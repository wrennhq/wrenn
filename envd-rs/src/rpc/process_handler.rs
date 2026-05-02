use std::io::Read;
use std::os::unix::process::CommandExt;
use std::process::Stdio;
use std::sync::{Arc, Mutex};

use connectrpc::{ConnectError, ErrorCode};
use nix::pty::{openpty, Winsize};
use nix::sys::signal::{self, Signal};
use nix::unistd::Pid;
use tokio::sync::broadcast;

use crate::rpc::pb::process::*;

const STD_CHUNK_SIZE: usize = 32768;
const PTY_CHUNK_SIZE: usize = 16384;
const BROADCAST_CAPACITY: usize = 4096;

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

pub struct ProcessHandle {
    pub config: ProcessConfig,
    pub tag: Option<String>,
    pub pid: u32,

    data_tx: broadcast::Sender<DataEvent>,
    end_tx: broadcast::Sender<EndEvent>,

    stdin: Mutex<Option<std::process::ChildStdin>>,
    pty_master: Mutex<Option<std::fs::File>>,
}

impl ProcessHandle {
    pub fn subscribe_data(&self) -> broadcast::Receiver<DataEvent> {
        self.data_tx.subscribe()
    }

    pub fn subscribe_end(&self) -> broadcast::Receiver<EndEvent> {
        self.end_tx.subscribe()
    }

    pub fn send_signal(&self, sig: Signal) -> Result<(), ConnectError> {
        signal::kill(Pid::from_raw(self.pid as i32), sig).map_err(|e| {
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
) -> Result<Arc<ProcessHandle>, ConnectError> {
    let mut env: Vec<(String, String)> = Vec::new();
    env.push(("PATH".into(), std::env::var("PATH").unwrap_or_default()));
    let home = user.dir.to_string_lossy().to_string();
    env.push(("HOME".into(), home));
    env.push(("USER".into(), user.name.clone()));
    env.push(("LOGNAME".into(), user.name.clone()));

    default_env_vars.iter().for_each(|entry| {
        env.push((entry.key().clone(), entry.value().clone()));
    });

    for (k, v) in envs {
        env.push((k.clone(), v.clone()));
    }

    let nice_delta = 0 - current_nice();
    let oom_script = format!(
        r#"echo 100 > /proc/$$/oom_score_adj && exec /usr/bin/nice -n {} "${{@}}""#,
        nice_delta
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

        let mut command = std::process::Command::new("/bin/sh");
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
            ConnectError::new(ErrorCode::Internal, format!("error starting pty process: {e}"))
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
            stdin: Mutex::new(None),
            pty_master: Mutex::new(Some(master_file)),
        });

        let data_tx_clone = data_tx.clone();
        std::thread::spawn(move || {
            let mut master = master_clone;
            let mut buf = vec![0u8; PTY_CHUNK_SIZE];
            loop {
                match master.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => {
                        let _ = data_tx_clone.send(DataEvent::Pty(buf[..n].to_vec()));
                    }
                    Err(_) => break,
                }
            }
        });

        let end_tx_clone = end_tx.clone();
        std::thread::spawn(move || {
            let mut child = child;
            match child.wait() {
                Ok(s) => {
                    let _ = end_tx_clone.send(EndEvent {
                        exit_code: s.code().unwrap_or(-1),
                        exited: s.code().is_some(),
                        status: format!("{s}"),
                        error: None,
                    });
                }
                Err(e) => {
                    let _ = end_tx_clone.send(EndEvent {
                        exit_code: -1,
                        exited: false,
                        status: "error".into(),
                        error: Some(e.to_string()),
                    });
                }
            }
        });

        tracing::info!(pid, cmd = cmd_str, "process started (pty)");
        Ok(handle)
    } else {
        let mut command = std::process::Command::new("/bin/sh");
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
            stdin: Mutex::new(stdin),
            pty_master: Mutex::new(None),
        });

        if let Some(mut out) = stdout {
            let tx = data_tx.clone();
            std::thread::spawn(move || {
                let mut buf = vec![0u8; STD_CHUNK_SIZE];
                loop {
                    match out.read(&mut buf) {
                        Ok(0) => break,
                        Ok(n) => {
                            let _ = tx.send(DataEvent::Stdout(buf[..n].to_vec()));
                        }
                        Err(_) => break,
                    }
                }
            });
        }

        if let Some(mut err_pipe) = stderr {
            let tx = data_tx.clone();
            std::thread::spawn(move || {
                let mut buf = vec![0u8; STD_CHUNK_SIZE];
                loop {
                    match err_pipe.read(&mut buf) {
                        Ok(0) => break,
                        Ok(n) => {
                            let _ = tx.send(DataEvent::Stderr(buf[..n].to_vec()));
                        }
                        Err(_) => break,
                    }
                }
            });
        }

        let end_tx_clone = end_tx.clone();
        std::thread::spawn(move || {
            match child.wait() {
                Ok(s) => {
                    let _ = end_tx_clone.send(EndEvent {
                        exit_code: s.code().unwrap_or(-1),
                        exited: s.code().is_some(),
                        status: format!("{s}"),
                        error: None,
                    });
                }
                Err(e) => {
                    let _ = end_tx_clone.send(EndEvent {
                        exit_code: -1,
                        exited: false,
                        status: "error".into(),
                        error: Some(e.to_string()),
                    });
                }
            }
        });

        tracing::info!(pid, cmd = cmd_str, "process started (pipe)");
        Ok(handle)
    }
}

fn current_nice() -> i32 {
    unsafe {
        *libc::__errno_location() = 0;
        let prio = libc::getpriority(libc::PRIO_PROCESS, 0);
        if *libc::__errno_location() != 0 {
            return 0;
        }
        20 - prio
    }
}
