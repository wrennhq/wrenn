use std::collections::VecDeque;
use std::io::Read;
use std::os::unix::process::CommandExt;
use std::process::Stdio;
use std::sync::{Arc, Mutex};

use connectrpc::{ConnectError, ErrorCode};
use nix::pty::{Winsize, openpty};
use nix::sys::signal::{self, Signal};
use nix::unistd::Pid;
use tokio::sync::broadcast;

use crate::rpc::pb::process::*;

const STD_CHUNK_SIZE: usize = 32768;
const PTY_CHUNK_SIZE: usize = 16384;
const BROADCAST_CAPACITY: usize = 4096;

// Upper bound on the per-process output kept for replay. A late Connect gets
// the most recent OUTPUT_LOG_CAPACITY bytes (older output is evicted) so the
// buffer can never grow without bound for a chatty long-running process.
const OUTPUT_LOG_CAPACITY: usize = 256 * 1024;

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

    pub fn write_stdin(&self, data: &[u8]) -> Result<(), ConnectError> {
        use std::io::Write;
        let mut guard = self.stdin.lock().unwrap();
        match guard.as_mut() {
            Some(stdin) => stdin.write_all(data).map_err(|e| {
                ConnectError::new(ErrorCode::Internal, format!("error writing to stdin: {e}"))
            }),
            None => Err(ConnectError::new(
                ErrorCode::FailedPrecondition,
                "stdin not enabled or closed",
            )),
        }
    }

    pub fn write_pty(&self, data: &[u8]) -> Result<(), ConnectError> {
        use std::io::Write;
        let mut guard = self.pty_master.lock().unwrap();
        match guard.as_mut() {
            Some(master) => master.write_all(data).map_err(|e| {
                ConnectError::new(ErrorCode::Internal, format!("error writing to pty: {e}"))
            }),
            None => Err(ConnectError::new(
                ErrorCode::FailedPrecondition,
                "pty not assigned to process",
            )),
        }
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
    let target = if args.is_empty() {
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

        let mut command = std::process::Command::new("/bin/bash");
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
        });

        let data_rx = handle.subscribe_data();
        let end_rx = handle.subscribe_end();

        let handle_for_reader = Arc::clone(&handle);
        let pty_reader = std::thread::spawn(move || {
            let mut master = master_clone;
            let mut buf = vec![0u8; PTY_CHUNK_SIZE];
            loop {
                match master.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => {
                        handle_for_reader.publish_data(DataEvent::Pty(buf[..n].to_vec()));
                    }
                    Err(_) => break,
                }
            }
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
        let mut command = std::process::Command::new("/bin/bash");
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
        });

        let data_rx = handle.subscribe_data();
        let end_rx = handle.subscribe_end();

        let mut output_readers: Vec<std::thread::JoinHandle<()>> = Vec::new();

        if let Some(mut out) = stdout {
            let handle_for_reader = Arc::clone(&handle);
            output_readers.push(std::thread::spawn(move || {
                let mut buf = vec![0u8; STD_CHUNK_SIZE];
                loop {
                    match out.read(&mut buf) {
                        Ok(0) => break,
                        Ok(n) => {
                            handle_for_reader.publish_data(DataEvent::Stdout(buf[..n].to_vec()));
                        }
                        Err(_) => break,
                    }
                }
            }));
        }

        if let Some(mut err_pipe) = stderr {
            let handle_for_reader = Arc::clone(&handle);
            output_readers.push(std::thread::spawn(move || {
                let mut buf = vec![0u8; STD_CHUNK_SIZE];
                loop {
                    match err_pipe.read(&mut buf) {
                        Ok(0) => break,
                        Ok(n) => {
                            handle_for_reader.publish_data(DataEvent::Stderr(buf[..n].to_vec()));
                        }
                        Err(_) => break,
                    }
                }
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
