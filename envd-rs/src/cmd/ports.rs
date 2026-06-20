//! `envd ports` — list the open ports inside the sandbox that are reachable
//! from outside, alongside the URL each is served at.
//!
//! Runs as a one-shot client (not the daemon): it scans `/proc/net/tcp[6]`
//! directly via the shared port helper and reads the sandbox identity that the
//! daemon recorded under /run/wrenn at /init time. It refuses to run outside a
//! wrenn sandbox.

use std::fs;
use std::path::Path;

use crate::config::{DEFAULT_PORT, DEFAULT_PROXY_DOMAIN, WRENN_RUN_DIR};
use crate::port::conn::reachable_listening_ports;

/// Arguments for the `envd ports` subcommand.
#[derive(clap::Args)]
pub struct PortsArgs {
    /// Override the proxy domain used to build URLs (default: the domain
    /// injected by the host, falling back to the built-in default).
    #[arg(long)]
    domain: Option<String>,

    /// Emit JSON instead of a table.
    #[arg(long)]
    json: bool,
}

#[derive(serde::Serialize)]
struct PortEntry {
    port: u32,
    url: String,
}

/// Runs the subcommand and returns the desired process exit code.
pub fn run(args: &PortsArgs) -> i32 {
    if !inside_sandbox() {
        eprintln!("envd ports: not running inside a wrenn sandbox");
        return 1;
    }

    let sandbox_id = read_identity("WRENN_SANDBOX_ID", ".WRENN_SANDBOX_ID");
    let domain = args
        .domain
        .clone()
        .filter(|d| !d.is_empty())
        .or_else(|| read_identity("WRENN_PROXY_DOMAIN", ".WRENN_PROXY_DOMAIN"))
        .unwrap_or_else(|| DEFAULT_PROXY_DOMAIN.to_string());

    let entries: Vec<PortEntry> = reachable_listening_ports(DEFAULT_PORT as u32)
        .into_iter()
        .map(|port| PortEntry {
            url: build_url(port, sandbox_id.as_deref(), &domain),
            port,
        })
        .collect();

    if args.json {
        match serde_json::to_string_pretty(&entries) {
            Ok(s) => println!("{s}"),
            Err(e) => {
                eprintln!("envd ports: failed to encode JSON: {e}");
                return 1;
            }
        }
        return 0;
    }

    if entries.is_empty() {
        println!("No open ports.");
        return 0;
    }

    println!("{:<6} {}", "PORT", "URL");
    for e in &entries {
        println!("{:<6} {}", e.port, e.url);
    }
    0
}

/// A wrenn sandbox is identified by the marker the daemon writes at startup
/// (`/run/wrenn/.WRENN_SANDBOX`) and the `WRENN_SANDBOX` env var it exports
/// into spawned processes. Running `envd ports` on a normal host finds neither
/// and is refused.
fn inside_sandbox() -> bool {
    if std::env::var("WRENN_SANDBOX").as_deref() == Ok("true") {
        return true;
    }
    Path::new(WRENN_RUN_DIR).join(".WRENN_SANDBOX").exists()
}

/// Reads an identity value from the environment, falling back to the matching
/// /run/wrenn file. Returns None when neither is set or both are blank.
fn read_identity(env_key: &str, file_name: &str) -> Option<String> {
    if let Ok(v) = std::env::var(env_key) {
        let v = v.trim().to_string();
        if !v.is_empty() {
            return Some(v);
        }
    }
    match fs::read_to_string(Path::new(WRENN_RUN_DIR).join(file_name)) {
        Ok(v) => {
            let v = v.trim().to_string();
            if v.is_empty() { None } else { Some(v) }
        }
        Err(_) => None,
    }
}

/// Builds the externally-reachable URL for a port. With a known sandbox ID the
/// result is a working https URL; without it (identity not yet injected) the
/// sandbox-ID segment degrades to a `<sandbox-id>` placeholder so output is
/// still informative.
fn build_url(port: u32, sandbox_id: Option<&str>, domain: &str) -> String {
    let id = sandbox_id.unwrap_or("<sandbox-id>");
    format!("https://{port}-{id}.{domain}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn url_with_sandbox_id() {
        assert_eq!(
            build_url(8000, Some("cl-abcd1234"), "wrenn.dev"),
            "https://8000-cl-abcd1234.wrenn.dev"
        );
    }

    #[test]
    fn url_without_sandbox_id_uses_placeholder() {
        assert_eq!(
            build_url(5173, None, "wrenn.dev"),
            "https://5173-<sandbox-id>.wrenn.dev"
        );
    }

    #[test]
    fn url_honors_custom_domain() {
        assert_eq!(
            build_url(3000, Some("cl-deadbeef"), "sandbox.example.com"),
            "https://3000-cl-deadbeef.sandbox.example.com"
        );
    }

    #[test]
    fn read_identity_prefers_env() {
        // SAFETY: test-local env var, single-threaded test body.
        unsafe { std::env::set_var("ENVD_PORTS_TEST_ID", "  cl-fromenv  ") };
        assert_eq!(
            read_identity("ENVD_PORTS_TEST_ID", ".nonexistent-file"),
            Some("cl-fromenv".to_string())
        );
        unsafe { std::env::remove_var("ENVD_PORTS_TEST_ID") };
    }

    #[test]
    fn read_identity_none_when_unset() {
        assert_eq!(
            read_identity("ENVD_PORTS_TEST_UNSET", ".nonexistent-file"),
            None
        );
    }
}
