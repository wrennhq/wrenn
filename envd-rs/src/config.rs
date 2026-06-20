use std::time::Duration;

pub const DEFAULT_PORT: u16 = 49983;
pub const IDLE_TIMEOUT: Duration = Duration::from_secs(640);
pub const CORS_MAX_AGE: Duration = Duration::from_secs(7200);
pub const PORT_SCANNER_INTERVAL: Duration = Duration::from_millis(1000);
pub const DEFAULT_USER: &str = "root";
pub const WRENN_RUN_DIR: &str = "/run/wrenn";

/// Fallback proxy domain used by `envd ports` to build URLs when the host has
/// not injected one via /init. Matches the host agent's WRENN_PROXY_DOMAIN
/// default.
pub const DEFAULT_PROXY_DOMAIN: &str = "wrenn.dev";

pub const KILOBYTE: u64 = 1024;
pub const MEGABYTE: u64 = 1024 * KILOBYTE;
