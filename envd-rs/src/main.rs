#![allow(dead_code)]

mod auth;
mod cgroups;
mod config;
mod conntracker;
mod crypto;
mod execcontext;
mod host;
mod http;
mod logging;
mod permissions;
mod port;
mod rpc;
mod state;
mod util;

use std::fs;
use std::net::SocketAddr;
use std::path::Path;
use std::sync::Arc;

use clap::Parser;
use tokio::net::TcpListener;
use tokio_util::sync::CancellationToken;

use config::{DEFAULT_PORT, DEFAULT_USER, WRENN_RUN_DIR};
use execcontext::Defaults;
use port::subsystem::PortSubsystem;
use state::AppState;

const VERSION: &str = env!("CARGO_PKG_VERSION");

const COMMIT: &str = {
    match option_env!("ENVD_COMMIT") {
        Some(c) => c,
        None => "unknown",
    }
};

#[derive(Parser)]
#[command(name = "envd", about = "Wrenn guest agent daemon")]
struct Cli {
    #[arg(long, default_value_t = DEFAULT_PORT)]
    port: u16,

    #[arg(long = "isnotfc", default_value_t = false)]
    is_not_fc: bool,

    #[arg(long)]
    version: bool,

    #[arg(long)]
    commit: bool,

    #[arg(long = "cmd", default_value = "")]
    start_cmd: String,

    #[arg(long = "cgroup-root", default_value = "/sys/fs/cgroup")]
    cgroup_root: String,
}

#[tokio::main]
async fn main() {
    let cli = Cli::parse();

    if cli.version {
        println!("{VERSION}");
        return;
    }
    if cli.commit {
        println!("{COMMIT}");
        return;
    }

    let use_json = !cli.is_not_fc;
    logging::init(use_json);

    if let Err(e) = fs::create_dir_all(WRENN_RUN_DIR) {
        tracing::error!(error = %e, "failed to create wrenn run directory");
    }

    let defaults = Defaults::new(DEFAULT_USER);
    let is_fc_str = if cli.is_not_fc { "false" } else { "true" };
    defaults
        .env_vars
        .insert("WRENN_SANDBOX".into(), is_fc_str.into());

    let wrenn_sandbox_path = Path::new(WRENN_RUN_DIR).join(".WRENN_SANDBOX");
    if let Err(e) = fs::write(&wrenn_sandbox_path, is_fc_str.as_bytes()) {
        tracing::error!(error = %e, "failed to write sandbox file");
    }

    let cancel = CancellationToken::new();

    // MMDS polling (only in FC mode)
    if !cli.is_not_fc {
        let env_vars = Arc::clone(&defaults.env_vars);
        let cancel_clone = cancel.clone();
        tokio::spawn(async move {
            host::mmds::poll_for_opts(env_vars, cancel_clone).await;
        });
    }

    // Cgroup manager
    let cgroup_manager: Arc<dyn cgroups::CgroupManager> =
        match cgroups::Cgroup2Manager::new(
            &cli.cgroup_root,
            &[
                (
                    cgroups::ProcessType::Pty,
                    "wrenn/pty",
                    &[] as &[(&str, &str)],
                ),
                (
                    cgroups::ProcessType::User,
                    "wrenn/user",
                    &[] as &[(&str, &str)],
                ),
                (
                    cgroups::ProcessType::Socat,
                    "wrenn/socat",
                    &[] as &[(&str, &str)],
                ),
            ],
        ) {
            Ok(m) => {
                tracing::info!("cgroup2 manager initialized");
                Arc::new(m)
            }
            Err(e) => {
                tracing::warn!(error = %e, "cgroup2 init failed, using noop");
                Arc::new(cgroups::NoopCgroupManager)
            }
        };

    // Port subsystem
    let port_subsystem = Arc::new(PortSubsystem::new(Arc::clone(&cgroup_manager)));
    port_subsystem.start();
    tracing::info!("port subsystem started");

    let state = AppState::new(
        defaults,
        VERSION.to_string(),
        COMMIT.to_string(),
        !cli.is_not_fc,
        Some(Arc::clone(&port_subsystem)),
    );

    // RPC services (Connect protocol — serves Connect + gRPC + gRPC-Web on same port)
    let connect_router = rpc::rpc_router(Arc::clone(&state));

    let app = http::router(Arc::clone(&state))
        .fallback_service(connect_router.into_axum_service());

    // --cmd: spawn initial process if specified
    if !cli.start_cmd.is_empty() {
        let cmd = cli.start_cmd.clone();
        let state_clone = Arc::clone(&state);
        tokio::spawn(async move {
            spawn_initial_command(&cmd, &state_clone);
        });
    }

    let addr = SocketAddr::from(([0, 0, 0, 0], cli.port));
    tracing::info!(port = cli.port, version = VERSION, commit = COMMIT, "envd starting");

    let listener = TcpListener::bind(addr).await.expect("failed to bind");

    let graceful = axum::serve(listener, app).with_graceful_shutdown(async move {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to register SIGTERM")
            .recv()
            .await;
        tracing::info!("SIGTERM received, shutting down");
    });

    if let Err(e) = graceful.await {
        tracing::error!(error = %e, "server error");
    }

    port_subsystem.stop();
    cancel.cancel();
}

fn spawn_initial_command(cmd: &str, state: &AppState) {
    use crate::permissions::user::lookup_user;
    use crate::rpc::process_handler;
    use std::collections::HashMap;

    let user = match lookup_user(&state.defaults.user) {
        Ok(u) => u,
        Err(e) => {
            tracing::error!(error = %e, "cmd: failed to lookup user");
            return;
        }
    };

    let home = user.dir.to_string_lossy().to_string();
    let cwd = state
        .defaults
        .workdir
        .as_deref()
        .unwrap_or(&home);

    match process_handler::spawn_process(
        cmd,
        &[],
        &HashMap::new(),
        cwd,
        None,
        false,
        Some("init-cmd".to_string()),
        &user,
        &state.defaults.env_vars,
    ) {
        Ok(handle) => {
            tracing::info!(pid = handle.pid, cmd, "initial command spawned");
        }
        Err(e) => {
            tracing::error!(error = %e, cmd, "failed to spawn initial command");
        }
    }
}
