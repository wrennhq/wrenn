use std::sync::Arc;

use tokio_util::sync::CancellationToken;

use crate::cgroups::CgroupManager;
use crate::config::PORT_SCANNER_INTERVAL;

use super::forwarder::Forwarder;
use super::scanner::{Scanner, ScannerFilter};

pub struct PortSubsystem {
    cgroup_manager: Arc<dyn CgroupManager>,
    cancel: std::sync::Mutex<Option<CancellationToken>>,
}

impl PortSubsystem {
    pub fn new(cgroup_manager: Arc<dyn CgroupManager>) -> Self {
        Self {
            cgroup_manager,
            cancel: std::sync::Mutex::new(None),
        }
    }

    pub fn start(&self) {
        let mut guard = self.cancel.lock().unwrap();
        if guard.is_some() {
            return;
        }

        let cancel = CancellationToken::new();
        *guard = Some(cancel.clone());
        drop(guard);

        let cgroup_manager = Arc::clone(&self.cgroup_manager);
        let cancel_scanner = cancel.clone();
        let cancel_forwarder = cancel.clone();

        tokio::spawn(async move {
            let scanner = Arc::new(Scanner::new(PORT_SCANNER_INTERVAL));
            let rx = scanner.add_subscriber(
                "port-forwarder",
                Some(ScannerFilter {
                    ips: vec![
                        "127.0.0.1".to_string(),
                        "localhost".to_string(),
                        "::1".to_string(),
                    ],
                    state: "LISTEN".to_string(),
                }),
            );

            let scanner_clone = Arc::clone(&scanner);

            let scanner_handle = tokio::spawn(async move {
                scanner_clone.scan_and_broadcast(cancel_scanner).await;
            });

            let forwarder_handle = tokio::spawn(async move {
                let mut forwarder = Forwarder::new(cgroup_manager);
                forwarder.start_forwarding(rx, cancel_forwarder).await;
            });

            let _ = tokio::join!(scanner_handle, forwarder_handle);
        });
    }

    pub fn stop(&self) {
        let mut guard = self.cancel.lock().unwrap();
        if let Some(cancel) = guard.take() {
            cancel.cancel();
        }
    }

    pub fn restart(&self) {
        self.stop();
        self.start();
    }
}
