use std::collections::VecDeque;
use std::io::Read;
use std::os::fd::{AsFd, OwnedFd};
use std::os::unix::io::{AsRawFd, RawFd};
use std::os::unix::process::CommandExt;
use std::process::Stdio;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use connectrpc::{ConnectError, ErrorCode};
use nix::pty::{Winsize, openpty};
use nix::sys::signal::{self, Signal};
use nix::unistd::Pid;
use tokio::io::Interest;
use tokio::io::unix::AsyncFd;
use tokio::sync::broadcast;

use crate::rpc::pb::process::*;

const STD_CHUNK_SIZE: usize = 32768;
const PTY_CHUNK_SIZE: usize = 16384;
const BROADCAST_CAPACITY: usize = 4096;

// Coalescing window for output reads. After the first read of a burst we keep
// draining whatever is already available on the fd for up to COALESCE_FLUSH_MS
// (or until COALESCE_CAP bytes accumulate), then publish one merged chunk. This
// collapses a full-screen TUI redraw — which the app emits as many small writes
// — into a single stream message, cutting per-message framing/encoding overhead
// across the whole path. Total added latency per flush is bounded by the window.
const COALESCE_FLUSH_MS: u64 = 4;
const COALESCE_CAP: usize = 64 * 1024;

// Upper bound on the per-process output kept for replay. A late Connect gets
// the most recent OUTPUT_LOG_CAPACITY bytes (older output is evicted) so the
// buffer can never grow without bound for a chatty long-running process.
const OUTPUT_LOG_CAPACITY: usize = 256 * 1024;

// Bound on how long one input write may wait for the target process to drain
// its buffer. The stdin pipe / pty master is O_NONBLOCK, so a full buffer
// parks the writing task (never an OS thread); within this window a
// briefly-full buffer recovers transparently, past it the write fails with
// ResourceExhausted instead of hanging forever on a process that stopped
// reading its input.
const INPUT_WRITE_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Clone)]
pub enum DataEvent {
    Stdout(Vec<u8>),
    Stderr(Vec<u8>),
    Pty(Vec<u8>),
}

#[derive(Clone)]
pub struct EndEvent {
    pub exit_code: i32,
    pub exited: bool,
    pub status: String,
    pub error: Option<String>,
}

/// Bounded ring of recent output, kept so a late Connect can replay what it
/// missed. Evicts oldest events once the retained bytes exceed the cap.
#[derive(Default)]
struct OutputLog {
    events: VecDeque<DataEvent>,
    bytes: usize,
}

impl OutputLog {
    fn push(&mut self, ev: &DataEvent) {
        self.bytes += ev_len(ev);
        self.events.push_back(ev.clone());
        while self.bytes > OUTPUT_LOG_CAPACITY {
            match self.events.pop_front() {
                Some(old) => self.bytes -= ev_len(&old),
                None => break,
            }
        }
    }

    fn snapshot(&self) -> Vec<DataEvent> {
        self.events.iter().cloned().collect()
    }
}

fn ev_len(ev: &DataEvent) -> usize {
    match ev {
        DataEvent::Stdout(d) | DataEvent::Stderr(d) | DataEvent::Pty(d) => d.len(),
    }
}

/// Blocking read loop that coalesces bursts: once a read returns, keep draining
/// whatever is already available on the fd (bounded by [`COALESCE_FLUSH_MS`] and
/// [`COALESCE_CAP`]) before handing the accumulated bytes to `publish`. A full
/// TUI redraw — emitted by the app as many small writes — collapses into one
/// output message instead of dozens. Latency added per flush is bounded by the
/// window, so interactive feel is preserved even for a slow trickle of output.
fn coalesce_read_loop<R, F>(mut reader: R, chunk_size: usize, mut publish: F)
where
    R: Read + AsRawFd,
    F: FnMut(Vec<u8>),
{
    let fd = reader.as_raw_fd();
    let mut rbuf = vec![0u8; chunk_size];
    let mut acc: Vec<u8> = Vec::new();
    loop {
        match reader.read(&mut rbuf) {
            Ok(0) => break,
            Ok(n) => {
                acc.extend_from_slice(&rbuf[..n]);
                let deadline = Instant::now() + Duration::from_millis(COALESCE_FLUSH_MS);
                while acc.len() < COALESCE_CAP {
                    let remaining = deadline.saturating_duration_since(Instant::now());
                    if remaining.is_zero() {
                        break;
                    }
                    let mut pfd = libc::pollfd {
                        fd,
                        events: libc::POLLIN,
                        revents: 0,
                    };
                    let timeout_ms = remaining.as_millis().min(i32::MAX as u128) as i32;
                    let r = unsafe { libc::poll(&mut pfd, 1, timeout_ms) };
                    if r < 0 {
                        // Interrupted by a signal (envd runs as PID 1, so SIGCHLD
                        // et al. land here): retry within the remaining window
                        // instead of cutting the coalesce burst short.
                        let errno = std::io::Error::last_os_error().raw_os_error();
                        if errno == Some(libc::EINTR) {
                            continue;
                        }
                        break;
                    }
                    if r == 0 || (pfd.revents & libc::POLLIN) == 0 {
                        break;
                    }
                    match reader.read(&mut rbuf) {
                        Ok(0) => {
                            if !acc.is_empty() {
                                publish(std::mem::take(&mut acc));
                            }
                            return;
                        }
                        Ok(m) => acc.extend_from_slice(&rbuf[..m]),
                        Err(_) => break,
                    }
                }
                publish(std::mem::take(&mut acc));
            }
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                // The pty master is O_NONBLOCK for the input write path and
                // shares its file description with this reader. Wait for
                // readability and retry; on POLLHUP/POLLERR the retried read
                // returns 0/EIO and ends the loop.
                let mut pfd = libc::pollfd {
                    fd,
                    events: libc::POLLIN,
                    revents: 0,
                };
                if unsafe { libc::poll(&mut pfd, 1, -1) } < 0 {
                    let errno = std::io::Error::last_os_error().raw_os_error();
                    if errno != Some(libc::EINTR) {
                        break;
                    }
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
            Err(_) => break,
        }
    }
    if !acc.is_empty() {
        publish(acc);
    }
}

pub struct ProcessHandle {
    pub config: ProcessConfig,
    pub tag: Option<String>,
    pub pid: u32,

    data_tx: broadcast::Sender<DataEvent>,
    end_tx: broadcast::Sender<EndEvent>,
    ended: Mutex<Option<EndEvent>>,
    output_log: Mutex<OutputLog>,

    stdin: Mutex<Option<std::process::ChildStdin>>,
    pty_master: Mutex<Option<std::fs::File>>,
    // Serializes input writes so concurrent chunks land in order. A tokio
    // mutex: a writer parked on a full buffer holds it across an await, and
    // the next writer waits as a suspended task — no OS thread is pinned.
    input_gate: tokio::sync::Mutex<()>,
}

impl ProcessHandle {
    pub fn subscribe_data(&self) -> broadcast::Receiver<DataEvent> {
        self.data_tx.subscribe()
    }

    /// Append a chunk to the replay buffer and broadcast it live, under one
    /// lock. The shared lock is what makes [`subscribe_data_replay`] race-free:
    /// a concurrent attach sees this chunk either in its snapshot or on its live
    /// receiver — never both, never neither.
    pub fn publish_data(&self, ev: DataEvent) {
        let mut log = self.output_log.lock().unwrap();
        log.push(&ev);
        let _ = self.data_tx.send(ev);
    }

    /// Snapshot the buffered output and subscribe to live output atomically, so
    /// a late Connect replays what it missed and then continues live with no gap
    /// or duplicate across the handoff.
    pub fn subscribe_data_replay(&self) -> (Vec<DataEvent>, broadcast::Receiver<DataEvent>) {
        let log = self.output_log.lock().unwrap();
        let snapshot = log.snapshot();
        let rx = self.data_tx.subscribe();
        (snapshot, rx)
    }

    pub fn subscribe_end(&self) -> broadcast::Receiver<EndEvent> {
        self.end_tx.subscribe()
    }

    pub fn cached_end(&self) -> Option<EndEvent> {
        self.ended.lock().unwrap().clone()
    }

    pub fn send_signal(&self, sig: Signal) -> Result<(), ConnectError> {
        // Signal the whole process group (negative pid), not just the immediate
        // /bin/sh wrapper. Otherwise children the process spawned are orphaned
        // and keep running. Both spawn paths make the process a group leader
        // (setsid for pty, setpgid for pipe), so pgid == pid.
        signal::kill(Pid::from_raw(-(self.pid as i32)), sig).map_err(|e| {
            ConnectError::new(ErrorCode::Internal, format!("error sending signal: {e}"))
        })
    }

    pub async fn write_stdin(&self, data: &[u8]) -> Result<(), ConnectError> {
        let _ordered = self.input_gate.lock().await;
        // Dup the fd under the std mutex, then release it before awaiting:
        // close_stdin stays responsive while a write is parked, and the fd
        // stays valid for this write even if stdin is closed concurrently.
        let fd = {
            let guard = self.stdin.lock().unwrap();
            match guard.as_ref() {
                Some(stdin) => dup_writer_fd(stdin, "stdin")?,
                None => {
                    return Err(ConnectError::new(
                        ErrorCode::FailedPrecondition,
                        "stdin not enabled or closed",
                    ));
                }
            }
        };
        write_nonblocking(fd, data, "stdin").await
    }

    pub async fn write_pty(&self, data: &[u8]) -> Result<(), ConnectError> {
        let _ordered = self.input_gate.lock().await;
        let fd = {
            let guard = self.pty_master.lock().unwrap();
            match guard.as_ref() {
                Some(master) => dup_writer_fd(master, "pty")?,
                None => {
                    return Err(ConnectError::new(
                        ErrorCode::FailedPrecondition,
                        "pty not assigned to process",
                    ));
                }
            }
        };
        write_nonblocking(fd, data, "pty").await
    }

    pub fn close_stdin(&self) -> Result<(), ConnectError> {
        if self.pty_master.lock().unwrap().is_some() {
            return Err(ConnectError::new(
                ErrorCode::FailedPrecondition,
                "cannot close stdin for PTY process — send Ctrl+D (0x04) instead",
            ));
        }
        let mut guard = self.stdin.lock().unwrap();
        *guard = None;
        Ok(())
    }

    pub fn resize_pty(&self, cols: u16, rows: u16) -> Result<(), ConnectError> {
        let guard = self.pty_master.lock().unwrap();
        match guard.as_ref() {
            Some(master) => {
                use std::os::unix::io::AsRawFd;
                let ws = libc::winsize {
                    ws_row: rows,
                    ws_col: cols,
                    ws_xpixel: 0,
                    ws_ypixel: 0,
                };
                let ret = unsafe { libc::ioctl(master.as_raw_fd(), libc::TIOCSWINSZ, &ws) };
                if ret != 0 {
                    return Err(ConnectError::new(
                        ErrorCode::Internal,
                        format!(
                            "ioctl TIOCSWINSZ failed: {}",
                            std::io::Error::last_os_error()
                        ),
                    ));
                }
                Ok(())
            }
            None => Err(ConnectError::new(
                ErrorCode::FailedPrecondition,
                "tty not assigned to process",
            )),
        }
    }
}

fn dup_writer_fd(f: &impl AsFd, what: &str) -> Result<OwnedFd, ConnectError> {
    f.as_fd()
        .try_clone_to_owned()
        .map_err(|e| ConnectError::new(ErrorCode::Internal, format!("dup {what} fd: {e}")))
}

/// Write `data` to a non-blocking fd from async context. Small interactive
/// writes complete inline; a full buffer surfaces as WouldBlock and we await
/// writability with a deadline, so a process that stopped reading its input
/// costs a parked task — never a pinned OS thread.
async fn write_nonblocking(fd: OwnedFd, data: &[u8], what: &str) -> Result<(), ConnectError> {
    let afd = AsyncFd::with_interest(fd, Interest::WRITABLE)
        .map_err(|e| ConnectError::new(ErrorCode::Internal, format!("register {what} fd: {e}")))?;
    let deadline = tokio::time::Instant::now() + INPUT_WRITE_TIMEOUT;
    let mut written = 0usize;
    while written < data.len() {
        let rest = &data[written..];
        let n = unsafe {
            libc::write(
                afd.get_ref().as_raw_fd(),
                rest.as_ptr() as *const libc::c_void,
                rest.len(),
            )
        };
        if n >= 0 {
            written += n as usize;
            continue;
        }
        let err = std::io::Error::last_os_error();
        match err.kind() {
            std::io::ErrorKind::WouldBlock => {
                match tokio::time::timeout_at(deadline, afd.writable()).await {
                    Ok(Ok(mut ready)) => ready.clear_ready(),
                    Ok(Err(e)) => {
                        return Err(ConnectError::new(
                            ErrorCode::Internal,
                            format!("error waiting for {what} to become writable: {e}"),
                        ));
                    }
                    Err(_) => {
                        return Err(ConnectError::new(
                            ErrorCode::ResourceExhausted,
                            format!(
                                "{what} input buffer full ({written} of {} bytes written): process is not reading its input",
                                data.len()
                            ),
                        ));
                    }
                }
            }
            std::io::ErrorKind::Interrupted => {}
            _ => {
                return Err(ConnectError::new(
                    ErrorCode::Internal,
                    format!("error writing to {what}: {err}"),
                ));
            }
        }
    }
    Ok(())
}

fn set_nonblocking(fd: RawFd) -> std::io::Result<()> {
    let flags = unsafe { libc::fcntl(fd, libc::F_GETFL) };
    if flags < 0 {
        return Err(std::io::Error::last_os_error());
    }
    if unsafe { libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

pub struct SpawnedProcess {
    pub handle: Arc<ProcessHandle>,
    pub data_rx: broadcast::Receiver<DataEvent>,
    pub end_rx: broadcast::Receiver<EndEvent>,
}

pub fn spawn_process(
    cmd_str: &str,
    args: &[String],
    envs: &std::collections::HashMap<String, String>,
    cwd: &str,
    pty_opts: Option<(u16, u16)>,
    enable_stdin: bool,
    tag: Option<String>,
    user: &nix::unistd::User,
    default_env_vars: &dashmap::DashMap<String, String>,
) -> Result<SpawnedProcess, ConnectError> {
    let mut env: Vec<(String, String)> = Vec::new();
    env.push(("PATH".into(), std::env::var("PATH").unwrap_or_default()));
    let home = user.dir.to_string_lossy().to_string();
    env.push(("HOME".into(), home));
    env.push(("USER".into(), user.name.clone()));
    env.push(("LOGNAME".into(), user.name.clone()));
    if !user.shell.as_os_str().is_empty() {
        env.push(("SHELL".into(), user.shell.to_string_lossy().to_string()));
    }

    default_env_vars.iter().for_each(|entry| {
        env.push((entry.key().clone(), entry.value().clone()));
    });

    for (k, v) in envs {
        env.push((k.clone(), v.clone()));
    }

    // Reset the child's nice value only when envd itself was started at an
    // elevated nice value (delta > 0 means raising the nice number / lowering
    // priority, which is permitted for non-root processes). A non-root process
    // cannot improve its priority, so skip the `nice` wrapper otherwise — it
    // would fail with EPERM ("cannot set niceness: permission denied") for
    // commands run as a non-root user. Writing 100 to the process's own
    // oom_score_adj is always permitted (raising the score).
    let nice_delta = 0 - current_nice();
    let profile_source = r#"test -f /etc/profile && . /etc/profile
test -f "${HOME}/.bashrc" && . "${HOME}/.bashrc""#;

    // Resolve the user's login shell, falling back to /bin/sh. Commands without
    // explicit args are interpreted by this shell so pipes, quoting, escape
    // sequences, backslash line-continuations, and other shell syntax work
    // without the caller having to wrap them in `sh -c` themselves.
    let shell = {
        let s = user.shell.to_string_lossy();
        if s.is_empty() {
            "/bin/sh".to_string()
        } else {
            s.to_string()
        }
    };

    // What the wrapper finally exec's, after the optional `nice` prefix.
    //   - no args: run cmd_str as a shell command line via the login shell
    //     ($1 is cmd_str; $0 of the inner shell is the shell path).
    //   - with args: exec the program + args directly, no shell interpretation
    //     (backward-compatible program/argv form).
    let target = if cmd_str.is_empty() && args.is_empty() {
        // No command at all (e.g. an interactive PTY session with no explicit
        // command): launch the user's login shell directly. Under a pty its
        // stdin is a tty, so it starts interactively.
        format!(r#""{shell}""#)
    } else if args.is_empty() {
        format!(r#""{shell}" -c "$1" "{shell}""#)
    } else {
        r#""$@""#.to_string()
    };
    let nice_prefix = if nice_delta > 0 {
        format!("/usr/bin/nice -n {nice_delta} ")
    } else {
        String::new()
    };
    let oom_script = format!(
        r#"echo 100 > /proc/$$/oom_score_adj
{profile_source}
exec {nice_prefix}{target}"#
    );
    let mut wrapper_args = vec![
        "-c".to_string(),
        oom_script,
        "--".to_string(),
        cmd_str.to_string(),
    ];
    wrapper_args.extend_from_slice(args);

    let uid = user.uid.as_raw();
    let gid = user.gid.as_raw();

    let (data_tx, _) = broadcast::channel(BROADCAST_CAPACITY);
    let (end_tx, _) = broadcast::channel(16);

    let config = ProcessConfig {
        cmd: cmd_str.to_string(),
        args: args.to_vec(),
        envs: envs.clone(),
        cwd: Some(cwd.to_string()),
        ..Default::default()
    };

    if let Some((cols, rows)) = pty_opts {
        let pty_result = openpty(
            Some(&Winsize {
                ws_row: rows,
                ws_col: cols,
                ws_xpixel: 0,
                ws_ypixel: 0,
            }),
            None,
        )
        .map_err(|e| ConnectError::new(ErrorCode::Internal, format!("openpty failed: {e}")))?;

        let master_fd = pty_result.master;
        let slave_fd = pty_result.slave;

        let mut command = std::process::Command::new(&shell);
        command
            .args(&wrapper_args)
            .env_clear()
            .envs(env.iter().map(|(k, v)| (k.as_str(), v.as_str())))
            .current_dir(cwd);

        unsafe {
            use std::os::unix::io::AsRawFd;
            let slave_raw = slave_fd.as_raw_fd();
            let master_raw = master_fd.as_raw_fd();
            command.pre_exec(move || {
                libc::close(master_raw);
                nix::unistd::setsid()
                    .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))?;
                libc::ioctl(slave_raw, libc::TIOCSCTTY, 0);
                libc::dup2(slave_raw, 0);
                libc::dup2(slave_raw, 1);
                libc::dup2(slave_raw, 2);
                if slave_raw > 2 {
                    libc::close(slave_raw);
                }
                libc::setgid(gid);
                libc::setuid(uid);
                Ok(())
            });
        }

        command.stdin(Stdio::null());
        command.stdout(Stdio::null());
        command.stderr(Stdio::null());

        let child = command.spawn().map_err(|e| {
            ConnectError::new(
                ErrorCode::Internal,
                format!("error starting pty process: {e}"),
            )
        })?;

        drop(slave_fd);

        let pid = child.id();
        let master_file: std::fs::File = master_fd.into();
        // O_NONBLOCK is per file description, so it covers the write path and
        // the reader clone alike; coalesce_read_loop handles the resulting
        // WouldBlock by polling. Without it a write would block an OS thread,
        // so treat failure as a failed spawn.
        if let Err(e) = set_nonblocking(master_file.as_raw_fd()) {
            let mut child = child;
            let _ = signal::kill(Pid::from_raw(-(pid as i32)), Signal::SIGKILL);
            let _ = child.wait();
            return Err(ConnectError::new(
                ErrorCode::Internal,
                format!("set pty master non-blocking: {e}"),
            ));
        }
        let master_clone = master_file.try_clone().unwrap();

        let handle = Arc::new(ProcessHandle {
            config,
            tag,
            pid,
            data_tx: data_tx.clone(),
            end_tx: end_tx.clone(),
            ended: Mutex::new(None),
            output_log: Mutex::new(OutputLog::default()),
            stdin: Mutex::new(None),
            pty_master: Mutex::new(Some(master_file)),
            input_gate: tokio::sync::Mutex::new(()),
        });

        let data_rx = handle.subscribe_data();
        let end_rx = handle.subscribe_end();

        let handle_for_reader = Arc::clone(&handle);
        let pty_reader = std::thread::spawn(move || {
            coalesce_read_loop(master_clone, PTY_CHUNK_SIZE, |chunk| {
                handle_for_reader.publish_data(DataEvent::Pty(chunk));
            });
        });

        let end_tx_clone = end_tx.clone();
        let handle_for_waiter = Arc::clone(&handle);
        std::thread::spawn(move || {
            let mut child = child;
            let status = child.wait();
            // Drain the pty to EOF before publishing the end event so trailing
            // output is never lost to a process-exit/pty-read race.
            let _ = pty_reader.join();
            let end_event = match status {
                Ok(s) => EndEvent {
                    exit_code: s.code().unwrap_or(-1),
                    exited: s.code().is_some(),
                    status: format!("{s}"),
                    error: None,
                },
                Err(e) => EndEvent {
                    exit_code: -1,
                    exited: false,
                    status: "error".into(),
                    error: Some(e.to_string()),
                },
            };
            *handle_for_waiter.ended.lock().unwrap() = Some(end_event.clone());
            let _ = end_tx_clone.send(end_event);
        });

        tracing::info!(pid, cmd = cmd_str, "process started (pty)");
        Ok(SpawnedProcess {
            handle,
            data_rx,
            end_rx,
        })
    } else {
        let mut command = std::process::Command::new(&shell);
        command
            .args(&wrapper_args)
            .env_clear()
            .envs(env.iter().map(|(k, v)| (k.as_str(), v.as_str())))
            .current_dir(cwd)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());

        if enable_stdin {
            command.stdin(Stdio::piped());
        } else {
            command.stdin(Stdio::null());
        }

        unsafe {
            command.pre_exec(move || {
                // Become a process-group leader so SendSignal can kill the
                // whole group, not just this wrapper. The pty path gets this
                // for free via setsid().
                nix::unistd::setpgid(Pid::from_raw(0), Pid::from_raw(0))
                    .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))?;
                libc::setgid(gid);
                libc::setuid(uid);
                Ok(())
            });
        }

        let mut child = command.spawn().map_err(|e| {
            ConnectError::new(ErrorCode::Internal, format!("error starting process: {e}"))
        })?;

        let pid = child.id();
        let stdin = child.stdin.take();
        let stdout = child.stdout.take();
        let stderr = child.stderr.take();

        // Non-blocking stdin is what lets write_stdin fail fast instead of
        // pinning an OS thread when the process stops draining its input.
        // Only the pipe's write end is affected — the child's read end is a
        // separate file description.
        if let Some(s) = stdin.as_ref() {
            if let Err(e) = set_nonblocking(s.as_raw_fd()) {
                let _ = signal::kill(Pid::from_raw(-(pid as i32)), Signal::SIGKILL);
                let _ = child.wait();
                return Err(ConnectError::new(
                    ErrorCode::Internal,
                    format!("set stdin non-blocking: {e}"),
                ));
            }
        }

        let handle = Arc::new(ProcessHandle {
            config,
            tag,
            pid,
            data_tx: data_tx.clone(),
            end_tx: end_tx.clone(),
            ended: Mutex::new(None),
            output_log: Mutex::new(OutputLog::default()),
            stdin: Mutex::new(stdin),
            pty_master: Mutex::new(None),
            input_gate: tokio::sync::Mutex::new(()),
        });

        let data_rx = handle.subscribe_data();
        let end_rx = handle.subscribe_end();

        let mut output_readers: Vec<std::thread::JoinHandle<()>> = Vec::new();

        if let Some(out) = stdout {
            let handle_for_reader = Arc::clone(&handle);
            output_readers.push(std::thread::spawn(move || {
                coalesce_read_loop(out, STD_CHUNK_SIZE, |chunk| {
                    handle_for_reader.publish_data(DataEvent::Stdout(chunk));
                });
            }));
        }

        if let Some(err_pipe) = stderr {
            let handle_for_reader = Arc::clone(&handle);
            output_readers.push(std::thread::spawn(move || {
                coalesce_read_loop(err_pipe, STD_CHUNK_SIZE, |chunk| {
                    handle_for_reader.publish_data(DataEvent::Stderr(chunk));
                });
            }));
        }

        let end_tx_clone = end_tx.clone();
        let handle_for_waiter = Arc::clone(&handle);
        std::thread::spawn(move || {
            let status = child.wait();
            // Drain stdout/stderr to EOF before publishing the end event so
            // trailing output is never lost to a process-exit/pipe-read race.
            for reader in output_readers {
                let _ = reader.join();
            }
            let end_event = match status {
                Ok(s) => EndEvent {
                    exit_code: s.code().unwrap_or(-1),
                    exited: s.code().is_some(),
                    status: format!("{s}"),
                    error: None,
                },
                Err(e) => EndEvent {
                    exit_code: -1,
                    exited: false,
                    status: "error".into(),
                    error: Some(e.to_string()),
                },
            };
            *handle_for_waiter.ended.lock().unwrap() = Some(end_event.clone());
            let _ = end_tx_clone.send(end_event);
        });

        tracing::info!(pid, cmd = cmd_str, "process started (pipe)");
        Ok(SpawnedProcess {
            handle,
            data_rx,
            end_rx,
        })
    }
}

fn current_nice() -> i32 {
    unsafe {
        *libc::__errno_location() = 0;
        let prio = libc::getpriority(libc::PRIO_PROCESS, 0);
        if *libc::__errno_location() != 0 {
            return 0;
        }
        // getpriority(PRIO_PROCESS, 0) returns the nice value directly,
        // in the range [-20, 19]; the normal default is 0.
        prio
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn spawn(cmd: &str, enable_stdin: bool, pty: Option<(u16, u16)>) -> SpawnedProcess {
        let user = nix::unistd::User::from_uid(nix::unistd::getuid())
            .unwrap()
            .unwrap();
        spawn_process(
            cmd,
            &[],
            &HashMap::new(),
            "/tmp",
            pty,
            enable_stdin,
            None,
            &user,
            &dashmap::DashMap::new(),
        )
        .unwrap()
    }

    /// Wait until the accumulated bytes from `recv` contain `needle`.
    async fn recv_until_contains(
        rx: &mut broadcast::Receiver<DataEvent>,
        needle: &[u8],
        what: &str,
    ) -> Vec<u8> {
        let mut acc: Vec<u8> = Vec::new();
        tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                match rx.recv().await {
                    Ok(DataEvent::Stdout(d)) | Ok(DataEvent::Pty(d)) => {
                        acc.extend_from_slice(&d);
                        if acc.windows(needle.len()).any(|w| w == needle) {
                            break;
                        }
                    }
                    Ok(DataEvent::Stderr(_)) => {}
                    Err(broadcast::error::RecvError::Lagged(_)) => {}
                    Err(e) => panic!("{what}: data stream closed early: {e}"),
                }
            }
        })
        .await
        .unwrap_or_else(|_| panic!("{what}: output never contained expected bytes"));
        acc
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn write_to_stuck_stdin_errors_instead_of_hanging() {
        // sleep never reads stdin: the 64K pipe buffer fills and the write
        // must fail with ResourceExhausted after the bounded wait — the old
        // write_all here blocked a pool thread forever.
        let spawned = spawn("sleep 60", true, None);
        let handle = Arc::clone(&spawned.handle);
        let big = vec![b'x'; 1024 * 1024];
        let writer = tokio::spawn(async move { handle.write_stdin(&big).await });

        // While that write is parked, the agent must stay fully responsive:
        // a fresh process spawns, runs, and reports its exit.
        let mut quick = spawn("true", false, None);
        let end = tokio::time::timeout(Duration::from_secs(10), quick.end_rx.recv())
            .await
            .expect("agent unresponsive while stdin write pending")
            .expect("end event");
        assert!(end.exited);

        let err = tokio::time::timeout(INPUT_WRITE_TIMEOUT + Duration::from_secs(10), writer)
            .await
            .expect("stdin write hung past its deadline")
            .expect("writer task panicked")
            .expect_err("write into a full pipe nobody reads must error");
        assert!(
            matches!(err.code, ErrorCode::ResourceExhausted),
            "expected ResourceExhausted, got {:?}",
            err.code
        );

        let _ = spawned.handle.send_signal(Signal::SIGKILL);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn stdin_write_and_eof_still_work() {
        let mut spawned = spawn("cat", true, None);
        spawned.handle.write_stdin(b"hello\n").await.unwrap();
        recv_until_contains(&mut spawned.data_rx, b"hello\n", "cat stdout").await;

        // close_stdin must still deliver EOF with the non-blocking pipe.
        spawned.handle.close_stdin().unwrap();
        let end = tokio::time::timeout(Duration::from_secs(10), spawned.end_rx.recv())
            .await
            .expect("cat did not exit after stdin EOF")
            .expect("end event");
        assert_eq!(end.exit_code, 0);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn pty_write_and_echo_still_work() {
        // O_NONBLOCK on the pty master is shared with the output reader;
        // this covers both directions surviving it.
        let mut spawned = spawn("cat", false, Some((80, 24)));
        spawned.handle.write_pty(b"hello\r").await.unwrap();
        recv_until_contains(&mut spawned.data_rx, b"hello", "pty echo").await;
        let _ = spawned.handle.send_signal(Signal::SIGKILL);
    }
}
