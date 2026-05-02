use std::collections::HashMap;
use std::os::unix::process::CommandExt;
use std::process::Command;
use std::sync::Arc;

use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

use crate::cgroups::{CgroupManager, ProcessType};

use super::conn::ConnStat;

const DEFAULT_GATEWAY_IP: &str = "169.254.0.21";

#[derive(PartialEq)]
enum PortState {
    Forward,
    Delete,
}

struct PortToForward {
    pid: Option<u32>,
    inode: u64,
    family: u32,
    state: PortState,
    port: u32,
}

fn family_to_ip_version(family: u32) -> u32 {
    if family == libc::AF_INET as u32 {
        4
    } else if family == libc::AF_INET6 as u32 {
        6
    } else {
        0
    }
}

pub struct Forwarder {
    cgroup_manager: Arc<dyn CgroupManager>,
    ports: HashMap<String, PortToForward>,
    source_ip: String,
}

impl Forwarder {
    pub fn new(cgroup_manager: Arc<dyn CgroupManager>) -> Self {
        Self {
            cgroup_manager,
            ports: HashMap::new(),
            source_ip: DEFAULT_GATEWAY_IP.to_string(),
        }
    }

    pub async fn start_forwarding(
        &mut self,
        mut rx: mpsc::Receiver<Vec<ConnStat>>,
        cancel: CancellationToken,
    ) {
        loop {
            tokio::select! {
                _ = cancel.cancelled() => {
                    self.stop_all();
                    return;
                }
                msg = rx.recv() => {
                    match msg {
                        Some(conns) => self.process_scan(conns),
                        None => {
                            self.stop_all();
                            return;
                        }
                    }
                }
            }
        }
    }

    fn process_scan(&mut self, conns: Vec<ConnStat>) {
        for ptf in self.ports.values_mut() {
            ptf.state = PortState::Delete;
        }

        for conn in &conns {
            let key = format!("{}-{}", conn.inode, conn.local_port);
            if let Some(ptf) = self.ports.get_mut(&key) {
                ptf.state = PortState::Forward;
            } else {
                tracing::debug!(
                    ip = %conn.local_ip,
                    port = conn.local_port,
                    family = family_to_ip_version(conn.family),
                    "detected new port on localhost"
                );
                let mut ptf = PortToForward {
                    pid: None,
                    inode: conn.inode,
                    family: family_to_ip_version(conn.family),
                    state: PortState::Forward,
                    port: conn.local_port,
                };
                self.start_port_forwarding(&mut ptf);
                self.ports.insert(key, ptf);
            }
        }

        let to_stop: Vec<String> = self
            .ports
            .iter()
            .filter(|(_, v)| v.state == PortState::Delete)
            .map(|(k, _)| k.clone())
            .collect();

        for key in to_stop {
            if let Some(ptf) = self.ports.get(&key) {
                stop_port_forwarding(ptf);
            }
            self.ports.remove(&key);
        }
    }

    fn start_port_forwarding(&self, ptf: &mut PortToForward) {
        let listen_arg = format!(
            "TCP4-LISTEN:{},bind={},reuseaddr,fork",
            ptf.port, self.source_ip
        );
        let connect_arg = format!("TCP{}:localhost:{}", ptf.family, ptf.port);

        let mut cmd = Command::new("socat");
        cmd.args(["-d", "-d", "-d", &listen_arg, &connect_arg]);

        unsafe {
            let cgroup_fd = self.cgroup_manager.get_fd(ProcessType::Socat);
            cmd.pre_exec(move || {
                libc::setpgid(0, 0);
                if let Some(fd) = cgroup_fd {
                    let pid_str = format!("{}", libc::getpid());
                    let tasks_path = format!("/proc/self/fd/{}/cgroup.procs", fd);
                    let _ = std::fs::write(&tasks_path, pid_str.as_bytes());
                }
                Ok(())
            });
        }

        tracing::debug!(
            port = ptf.port,
            inode = ptf.inode,
            family = ptf.family,
            source_ip = %self.source_ip,
            "starting port forwarding"
        );

        match cmd.spawn() {
            Ok(child) => {
                ptf.pid = Some(child.id());
                std::thread::spawn(move || {
                    let mut child = child;
                    let _ = child.wait();
                });
            }
            Err(e) => {
                tracing::error!(error = %e, port = ptf.port, "failed to start socat");
            }
        }
    }

    fn stop_all(&mut self) {
        for ptf in self.ports.values() {
            stop_port_forwarding(ptf);
        }
        self.ports.clear();
    }
}

fn stop_port_forwarding(ptf: &PortToForward) {
    if let Some(pid) = ptf.pid {
        tracing::debug!(port = ptf.port, pid, "stopping port forwarding");
        unsafe {
            libc::kill(-(pid as i32), libc::SIGKILL);
        }
    }
}
