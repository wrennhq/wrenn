pub mod entry;
pub mod filesystem_service;
pub mod pb;
pub mod process_handler;
pub mod process_service;

use std::sync::Arc;

use crate::rpc::filesystem_service::FilesystemServiceImpl;
use crate::rpc::process_service::ProcessServiceImpl;
use crate::state::AppState;

use pb::filesystem::FilesystemExt;
use pb::process::ProcessExt;

/// Build the connect-rust Router with both RPC services registered.
pub fn rpc_router(state: Arc<AppState>) -> connectrpc::Router {
    let process_svc = Arc::new(ProcessServiceImpl::new(Arc::clone(&state)));
    let filesystem_svc = Arc::new(FilesystemServiceImpl::new(Arc::clone(&state)));

    let router = connectrpc::Router::new();
    let router = process_svc.register(router);
    let router = filesystem_svc.register(router);

    router
}
