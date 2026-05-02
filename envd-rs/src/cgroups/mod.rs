use std::collections::HashMap;
use std::fs;
use std::os::unix::io::{OwnedFd, RawFd};
use std::path::PathBuf;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum ProcessType {
    Pty,
    User,
    Socat,
}

pub trait CgroupManager: Send + Sync {
    fn get_fd(&self, proc_type: ProcessType) -> Option<RawFd>;
}

pub struct Cgroup2Manager {
    fds: HashMap<ProcessType, OwnedFd>,
}

impl Cgroup2Manager {
    pub fn new(root: &str, configs: &[(ProcessType, &str, &[(&str, &str)])]) -> Result<Self, String> {
        let mut fds = HashMap::new();

        for (proc_type, sub_path, properties) in configs {
            let full_path = PathBuf::from(root).join(sub_path);

            fs::create_dir_all(&full_path).map_err(|e| {
                format!("failed to create cgroup {}: {e}", full_path.display())
            })?;

            for (name, value) in *properties {
                let prop_path = full_path.join(name);
                fs::write(&prop_path, value).map_err(|e| {
                    format!("failed to write cgroup property {}: {e}", prop_path.display())
                })?;
            }

            let fd = nix::fcntl::open(
                &full_path,
                nix::fcntl::OFlag::O_RDONLY,
                nix::sys::stat::Mode::empty(),
            )
            .map_err(|e| format!("failed to open cgroup {}: {e}", full_path.display()))?;

            fds.insert(*proc_type, fd);
        }

        Ok(Self { fds })
    }
}

impl CgroupManager for Cgroup2Manager {
    fn get_fd(&self, proc_type: ProcessType) -> Option<RawFd> {
        use std::os::unix::io::AsRawFd;
        self.fds.get(&proc_type).map(|fd| fd.as_raw_fd())
    }
}

pub struct NoopCgroupManager;

impl CgroupManager for NoopCgroupManager {
    fn get_fd(&self, _proc_type: ProcessType) -> Option<RawFd> {
        None
    }
}
