use std::sync::{Arc, RwLock};
use std::time::Duration;

use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

use super::conn::{ConnStat, read_tcp_connections};

pub struct ScannerFilter {
    pub ips: Vec<String>,
    pub state: String,
}

impl ScannerFilter {
    pub fn matches(&self, conn: &ConnStat) -> bool {
        if self.state.is_empty() && self.ips.is_empty() {
            return false;
        }
        self.ips.contains(&conn.local_ip) && self.state == conn.status
    }
}

pub struct ScannerSubscriber {
    pub tx: mpsc::Sender<Vec<ConnStat>>,
    pub filter: Option<ScannerFilter>,
}

pub struct Scanner {
    period: Duration,
    subs: RwLock<Vec<(String, Arc<ScannerSubscriber>)>>,
}

impl Scanner {
    pub fn new(period: Duration) -> Self {
        Self {
            period,
            subs: RwLock::new(Vec::new()),
        }
    }

    pub fn add_subscriber(
        &self,
        id: &str,
        filter: Option<ScannerFilter>,
    ) -> mpsc::Receiver<Vec<ConnStat>> {
        let (tx, rx) = mpsc::channel(4);
        let sub = Arc::new(ScannerSubscriber { tx, filter });
        let mut subs = self.subs.write().unwrap();
        subs.push((id.to_string(), sub));
        rx
    }

    pub fn remove_subscriber(&self, id: &str) {
        let mut subs = self.subs.write().unwrap();
        subs.retain(|(sid, _)| sid != id);
    }

    pub async fn scan_and_broadcast(&self, cancel: CancellationToken) {
        loop {
            let conns = read_tcp_connections();

            {
                let subs = self.subs.read().unwrap();
                for (_, sub) in subs.iter() {
                    let payload = match &sub.filter {
                        Some(f) => conns.iter().filter(|c| f.matches(c)).cloned().collect(),
                        None => conns.clone(),
                    };
                    let _ = sub.tx.try_send(payload);
                }
            }

            tokio::select! {
                _ = cancel.cancelled() => return,
                _ = tokio::time::sleep(self.period) => {}
            }
        }
    }
}
