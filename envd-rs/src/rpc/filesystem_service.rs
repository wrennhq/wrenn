use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::{Arc, Mutex};

use connectrpc::{ConnectError, Context, ErrorCode};
use dashmap::DashMap;
use futures::Stream;

use crate::permissions::path::{ensure_dirs, expand_and_resolve};
use crate::permissions::user::lookup_user;
use crate::rpc::entry::build_entry_info;
use crate::rpc::pb::filesystem::*;
use crate::state::AppState;

pub struct FilesystemServiceImpl {
    state: Arc<AppState>,
    watchers: DashMap<String, WatcherHandle>,
}

struct WatcherHandle {
    events: Arc<Mutex<Vec<FilesystemEvent>>>,
    _watcher: notify::RecommendedWatcher,
}

impl FilesystemServiceImpl {
    pub fn new(state: Arc<AppState>) -> Self {
        Self {
            state,
            watchers: DashMap::new(),
        }
    }

    fn resolve_path(&self, path: &str, ctx: &Context) -> Result<String, ConnectError> {
        let username = extract_username(ctx).unwrap_or_else(|| self.state.defaults.user.clone());
        let user = lookup_user(&username).map_err(|e| {
            ConnectError::new(ErrorCode::Unauthenticated, format!("invalid user: {e}"))
        })?;

        let home_dir = user.dir.to_string_lossy().to_string();
        let default_workdir = self.state.defaults.workdir.as_deref();

        expand_and_resolve(path, &home_dir, default_workdir)
            .map_err(|e| ConnectError::new(ErrorCode::InvalidArgument, e))
    }
}

fn extract_username(ctx: &Context) -> Option<String> {
    ctx.extensions.get::<AuthUser>().map(|u| u.0.clone())
}

#[derive(Clone)]
pub struct AuthUser(pub String);

impl Filesystem for FilesystemServiceImpl {
    async fn stat(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<StatRequestView<'static>>,
    ) -> Result<(StatResponse, Context), ConnectError> {
        let path = self.resolve_path(request.path, &ctx)?;
        let entry = build_entry_info(&path)?;
        Ok((
            StatResponse {
                entry: entry.into(),
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn make_dir(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<MakeDirRequestView<'static>>,
    ) -> Result<(MakeDirResponse, Context), ConnectError> {
        let path = self.resolve_path(request.path, &ctx)?;

        match std::fs::metadata(&path) {
            Ok(meta) => {
                if meta.is_dir() {
                    return Err(ConnectError::new(
                        ErrorCode::AlreadyExists,
                        format!("directory already exists: {path}"),
                    ));
                }
                return Err(ConnectError::new(
                    ErrorCode::InvalidArgument,
                    format!("path exists but is not a directory: {path}"),
                ));
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => {
                return Err(ConnectError::new(
                    ErrorCode::Internal,
                    format!("error getting file info: {e}"),
                ));
            }
        }

        let username = extract_username(&ctx).unwrap_or_else(|| self.state.defaults.user.clone());
        let user =
            lookup_user(&username).map_err(|e| ConnectError::new(ErrorCode::Internal, e))?;

        ensure_dirs(&path, user.uid, user.gid)
            .map_err(|e| ConnectError::new(ErrorCode::Internal, e))?;

        let entry = build_entry_info(&path)?;
        Ok((
            MakeDirResponse {
                entry: entry.into(),
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn r#move(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<MoveRequestView<'static>>,
    ) -> Result<(MoveResponse, Context), ConnectError> {
        let source = self.resolve_path(request.source, &ctx)?;
        let destination = self.resolve_path(request.destination, &ctx)?;

        let username = extract_username(&ctx).unwrap_or_else(|| self.state.defaults.user.clone());
        let user =
            lookup_user(&username).map_err(|e| ConnectError::new(ErrorCode::Internal, e))?;

        if let Some(parent) = Path::new(&destination).parent() {
            ensure_dirs(&parent.to_string_lossy(), user.uid, user.gid)
                .map_err(|e| ConnectError::new(ErrorCode::Internal, e))?;
        }

        std::fs::rename(&source, &destination).map_err(|e| {
            if e.kind() == std::io::ErrorKind::NotFound {
                ConnectError::new(ErrorCode::NotFound, format!("source not found: {e}"))
            } else {
                ConnectError::new(ErrorCode::Internal, format!("error renaming: {e}"))
            }
        })?;

        let entry = build_entry_info(&destination)?;
        Ok((
            MoveResponse {
                entry: entry.into(),
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn list_dir(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<ListDirRequestView<'static>>,
    ) -> Result<(ListDirResponse, Context), ConnectError> {
        let mut depth = request.depth as usize;
        if depth == 0 {
            depth = 1;
        }

        let path = self.resolve_path(request.path, &ctx)?;

        let resolved = std::fs::canonicalize(&path).map_err(|e| {
            if e.kind() == std::io::ErrorKind::NotFound {
                ConnectError::new(ErrorCode::NotFound, format!("path not found: {e}"))
            } else {
                ConnectError::new(ErrorCode::Internal, format!("error resolving path: {e}"))
            }
        })?;
        let resolved_str = resolved.to_string_lossy().to_string();

        let meta = std::fs::metadata(&resolved).map_err(|e| {
            ConnectError::new(ErrorCode::Internal, format!("error getting file info: {e}"))
        })?;
        if !meta.is_dir() {
            return Err(ConnectError::new(
                ErrorCode::InvalidArgument,
                format!("path is not a directory: {path}"),
            ));
        }

        let entries = walk_dir(&path, &resolved_str, depth)?;
        Ok((
            ListDirResponse {
                entries,
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn remove(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<RemoveRequestView<'static>>,
    ) -> Result<(RemoveResponse, Context), ConnectError> {
        let path = self.resolve_path(request.path, &ctx)?;

        if let Err(e1) = std::fs::remove_dir_all(&path) {
            if let Err(e2) = std::fs::remove_file(&path) {
                return Err(ConnectError::new(
                    ErrorCode::Internal,
                    format!("error removing: {e1}; also tried as file: {e2}"),
                ));
            }
        }

        Ok((RemoveResponse { ..Default::default() }, ctx))
    }

    async fn watch_dir(
        &self,
        _ctx: Context,
        _request: buffa::view::OwnedView<WatchDirRequestView<'static>>,
    ) -> Result<
        (
            Pin<Box<dyn Stream<Item = Result<WatchDirResponse, ConnectError>> + Send>>,
            Context,
        ),
        ConnectError,
    > {
        Err(ConnectError::new(
            ErrorCode::Unimplemented,
            "watch_dir streaming not yet implemented",
        ))
    }

    async fn create_watcher(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<CreateWatcherRequestView<'static>>,
    ) -> Result<(CreateWatcherResponse, Context), ConnectError> {
        use notify::{RecursiveMode, Watcher};

        let path = self.resolve_path(request.path, &ctx)?;
        let recursive = request.recursive;

        if let Ok(true) = crate::rpc::entry::is_network_mount(&path) {
            return Err(ConnectError::new(
                ErrorCode::FailedPrecondition,
                "watching network mounts is not supported",
            ));
        }

        let watcher_id = simple_id();
        let events: Arc<Mutex<Vec<FilesystemEvent>>> = Arc::new(Mutex::new(Vec::new()));
        let events_cb = Arc::clone(&events);

        let mut watcher = notify::recommended_watcher(
            move |res: Result<notify::Event, notify::Error>| {
                if let Ok(event) = res {
                    let event_type = match event.kind {
                        notify::EventKind::Create(_) => EventType::EVENT_TYPE_CREATE,
                        notify::EventKind::Modify(notify::event::ModifyKind::Data(_)) => {
                            EventType::EVENT_TYPE_WRITE
                        }
                        notify::EventKind::Modify(notify::event::ModifyKind::Metadata(_)) => {
                            EventType::EVENT_TYPE_CHMOD
                        }
                        notify::EventKind::Remove(_) => EventType::EVENT_TYPE_REMOVE,
                        notify::EventKind::Modify(notify::event::ModifyKind::Name(_)) => {
                            EventType::EVENT_TYPE_RENAME
                        }
                        _ => return,
                    };

                    for p in &event.paths {
                        if let Ok(mut guard) = events_cb.lock() {
                            guard.push(FilesystemEvent {
                                name: p.to_string_lossy().to_string(),
                                r#type: buffa::EnumValue::Known(event_type),
                                ..Default::default()
                            });
                        }
                    }
                }
            },
        )
        .map_err(|e| {
            ConnectError::new(ErrorCode::Internal, format!("failed to create watcher: {e}"))
        })?;

        let mode = if recursive {
            RecursiveMode::Recursive
        } else {
            RecursiveMode::NonRecursive
        };

        watcher.watch(Path::new(&path), mode).map_err(|e| {
            ConnectError::new(ErrorCode::Internal, format!("failed to watch path: {e}"))
        })?;

        self.watchers.insert(
            watcher_id.clone(),
            WatcherHandle {
                events,
                _watcher: watcher,
            },
        );

        Ok((
            CreateWatcherResponse {
                watcher_id,
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn get_watcher_events(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<GetWatcherEventsRequestView<'static>>,
    ) -> Result<(GetWatcherEventsResponse, Context), ConnectError> {
        let watcher_id: &str = request.watcher_id;
        let handle = self.watchers.get(watcher_id).ok_or_else(|| {
            ConnectError::new(
                ErrorCode::NotFound,
                format!("watcher not found: {watcher_id}"),
            )
        })?;

        let events = {
            let mut guard = handle.events.lock().unwrap();
            std::mem::take(&mut *guard)
        };

        Ok((
            GetWatcherEventsResponse {
                events,
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn remove_watcher(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<RemoveWatcherRequestView<'static>>,
    ) -> Result<(RemoveWatcherResponse, Context), ConnectError> {
        let watcher_id: &str = request.watcher_id;
        self.watchers.remove(watcher_id);
        Ok((RemoveWatcherResponse { ..Default::default() }, ctx))
    }
}

fn walk_dir(
    requested_path: &str,
    resolved_path: &str,
    depth: usize,
) -> Result<Vec<EntryInfo>, ConnectError> {
    let mut entries = Vec::new();
    let base = Path::new(resolved_path);

    for result in walkdir::WalkDir::new(resolved_path)
        .min_depth(1)
        .max_depth(depth)
        .follow_links(false)
    {
        let dir_entry = match result {
            Ok(e) => e,
            Err(e) => {
                if e.io_error()
                    .is_some_and(|io| io.kind() == std::io::ErrorKind::NotFound)
                {
                    continue;
                }
                return Err(ConnectError::new(
                    ErrorCode::Internal,
                    format!("error reading directory: {e}"),
                ));
            }
        };

        let entry_path = dir_entry.path();
        let mut entry = match build_entry_info(&entry_path.to_string_lossy()) {
            Ok(e) => e,
            Err(e) if e.code == ErrorCode::NotFound => continue,
            Err(e) => return Err(e),
        };

        if let Ok(rel) = entry_path.strip_prefix(base) {
            let remapped = PathBuf::from(requested_path).join(rel);
            entry.path = remapped.to_string_lossy().to_string();
        }

        entries.push(entry);
    }

    Ok(entries)
}

fn simple_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    format!("w-{nanos:x}")
}
