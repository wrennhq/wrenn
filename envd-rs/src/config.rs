use std::time::Duration;

pub const DEFAULT_PORT: u16 = 49983;
pub const IDLE_TIMEOUT: Duration = Duration::from_secs(640);
pub const CORS_MAX_AGE: Duration = Duration::from_secs(7200);
pub const PORT_SCANNER_INTERVAL: Duration = Duration::from_millis(1000);
pub const DEFAULT_USER: &str = "root";
pub const WRENN_RUN_DIR: &str = "/run/wrenn";

pub const KILOBYTE: u64 = 1024;
pub const MEGABYTE: u64 = 1024 * KILOBYTE;
