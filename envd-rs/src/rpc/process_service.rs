use std::collections::HashMap;
use std::pin::Pin;
use std::sync::Arc;

use connectrpc::{ConnectError, Context, ErrorCode};
use dashmap::DashMap;
use futures::{Stream, StreamExt};
use tokio::sync::broadcast;

use crate::permissions::path::{expand_and_resolve, expand_tilde};
use crate::permissions::user::lookup_user;
use crate::rpc::pb::process::*;
use crate::rpc::process_handler::{self, DataEvent, ProcessHandle};
use crate::state::AppState;

pub struct ProcessServiceImpl {
    state: Arc<AppState>,
    processes: Arc<DashMap<u32, Arc<ProcessHandle>>>,
}

impl ProcessServiceImpl {
    pub fn new(state: Arc<AppState>) -> Self {
        Self {
            state,
            processes: Arc::new(DashMap::new()),
        }
    }

    fn get_process_by_selector(
        &self,
        selector: &ProcessSelectorView,
    ) -> Result<Arc<ProcessHandle>, ConnectError> {
        match &selector.selector {
            Some(process_selector::SelectorView::Pid(pid)) => {
                let pid_val = *pid;
                self.processes
                    .get(&pid_val)
                    .map(|entry| Arc::clone(entry.value()))
                    .ok_or_else(|| {
                        ConnectError::new(
                            ErrorCode::NotFound,
                            format!("process with pid {pid_val} not found"),
                        )
                    })
            }
            Some(process_selector::SelectorView::Tag(tag)) => {
                let tag_str: &str = tag;
                for entry in self.processes.iter() {
                    if let Some(ref t) = entry.value().tag {
                        if t == tag_str {
                            return Ok(Arc::clone(entry.value()));
                        }
                    }
                }
                Err(ConnectError::new(
                    ErrorCode::NotFound,
                    format!("process with tag {tag_str} not found"),
                ))
            }
            None => Err(ConnectError::new(
                ErrorCode::InvalidArgument,
                "process selector required",
            )),
        }
    }

    fn spawn_from_request(
        &self,
        request: &StartRequestView<'_>,
    ) -> Result<process_handler::SpawnedProcess, ConnectError> {
        let proc_config = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process config required")
        })?;

        // Per-request user overrides the sandbox default when provided.
        let username = if proc_config.user.is_empty() {
            self.state.defaults.user()
        } else {
            proc_config.user.to_string()
        };
        let user = lookup_user(&username).map_err(|e| ConnectError::new(ErrorCode::Internal, e))?;

        let cmd_raw: &str = proc_config.cmd;
        let args_raw: Vec<String> = proc_config.args.iter().map(|s| s.to_string()).collect();
        let envs: HashMap<String, String> = proc_config
            .envs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();

        let home_dir = user.dir.to_string_lossy().to_string();

        let cmd = expand_tilde(cmd_raw, &home_dir)
            .map_err(|e| ConnectError::new(ErrorCode::InvalidArgument, e))?;
        let args: Vec<String> = args_raw
            .into_iter()
            .map(|a| expand_tilde(&a, &home_dir).unwrap_or(a))
            .collect();

        let cwd_str: &str = proc_config.cwd.unwrap_or("");
        let default_workdir = self.state.defaults.workdir();
        let cwd = expand_and_resolve(cwd_str, &home_dir, default_workdir.as_deref())
            .map_err(|e| ConnectError::new(ErrorCode::InvalidArgument, e))?;

        let effective_cwd = if cwd.is_empty() { "/" } else { &cwd };
        if let Err(_) = std::fs::metadata(effective_cwd) {
            return Err(ConnectError::new(
                ErrorCode::InvalidArgument,
                format!("cwd '{effective_cwd}' does not exist"),
            ));
        }

        let pty_opts = request.pty.as_option().and_then(|pty| {
            pty.size
                .as_option()
                .map(|sz| (sz.cols as u16, sz.rows as u16))
        });

        let enable_stdin = request.stdin.unwrap_or(true);
        let tag = request.tag.map(|s| s.to_string());

        tracing::info!(
            cmd = cmd,
            has_pty = pty_opts.is_some(),
            pty_size = ?pty_opts,
            tag = ?tag,
            stdin = enable_stdin,
            cwd = effective_cwd,
            user = %username,
            "process.Start request"
        );

        let spawned = process_handler::spawn_process(
            &cmd,
            &args,
            &envs,
            effective_cwd,
            pty_opts,
            enable_stdin,
            tag,
            &user,
            &self.state.defaults.env_vars,
        )?;

        self.processes
            .insert(spawned.handle.pid, Arc::clone(&spawned.handle));

        let processes = Arc::clone(&self.processes);
        let pid = spawned.handle.pid;
        // Subscribe before checking cached_end so the prune cannot be lost to a
        // race: a short-lived process can exit and broadcast its end event
        // before this task runs. A broadcast receiver only sees messages sent
        // after subscribe(), so a late subscribe would miss the event forever
        // (recv() never returns Closed either — the handle keeps end_tx alive
        // until it leaves the map, which only this task does). The waiter sets
        // ended before sending end_tx, so cached_end() is a reliable fallback.
        let mut cleanup_end_rx = spawned.handle.subscribe_end();
        let already_ended = spawned.handle.cached_end().is_some();
        tokio::spawn(async move {
            if !already_ended {
                let _ = cleanup_end_rx.recv().await;
            }
            processes.remove(&pid);
        });

        Ok(spawned)
    }
}

impl Process for ProcessServiceImpl {
    async fn list(
        &self,
        ctx: Context,
        _request: buffa::view::OwnedView<ListRequestView<'static>>,
    ) -> Result<(ListResponse, Context), ConnectError> {
        let processes: Vec<ProcessInfo> = self
            .processes
            .iter()
            .map(|entry| {
                let h = entry.value();
                ProcessInfo {
                    config: buffa::MessageField::some(h.config.clone()),
                    pid: h.pid,
                    tag: h.tag.clone(),
                    ..Default::default()
                }
            })
            .collect();

        Ok((
            ListResponse {
                processes,
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn start(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<StartRequestView<'static>>,
    ) -> Result<
        (
            Pin<Box<dyn Stream<Item = Result<StartResponse, ConnectError>> + Send>>,
            Context,
        ),
        ConnectError,
    > {
        let spawned = self.spawn_from_request(&request)?;
        let pid = spawned.handle.pid;

        // Start subscribes before any output is produced, so there is nothing to
        // replay and the process cannot have ended yet.
        let stream = process_event_stream(pid, Vec::new(), spawned.data_rx, spawned.end_rx, None)
            .map(|r| r.map(wrap_start_response));

        Ok((Box::pin(stream), ctx))
    }

    async fn connect(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<ConnectRequestView<'static>>,
    ) -> Result<
        (
            Pin<Box<dyn Stream<Item = Result<ConnectResponse, ConnectError>> + Send>>,
            Context,
        ),
        ConnectError,
    > {
        let selector = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process selector required")
        })?;
        let handle = self.get_process_by_selector(selector)?;
        let pid = handle.pid;

        // Snapshot buffered output + subscribe live atomically, then read the
        // exit state. Ordering matters: end_rx must be subscribed before
        // cached_end is read so a process that exits in the window is still
        // observed (via the channel if subscribed in time, via cached_end
        // otherwise).
        let (replay, data_rx) = handle.subscribe_data_replay();
        let end_rx = handle.subscribe_end();
        let cached_end = handle.cached_end();

        let stream = process_event_stream(pid, replay, data_rx, end_rx, cached_end)
            .map(|r| r.map(wrap_connect_response));

        Ok((Box::pin(stream), ctx))
    }

    async fn update(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<UpdateRequestView<'static>>,
    ) -> Result<(UpdateResponse, Context), ConnectError> {
        let selector = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process selector required")
        })?;
        let handle = self.get_process_by_selector(selector)?;

        if let Some(pty) = request.pty.as_option() {
            if let Some(size) = pty.size.as_option() {
                handle.resize_pty(size.cols as u16, size.rows as u16)?;
            }
        }

        Ok((
            UpdateResponse {
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn stream_input(
        &self,
        ctx: Context,
        mut requests: Pin<
            Box<
                dyn Stream<
                        Item = Result<
                            buffa::view::OwnedView<StreamInputRequestView<'static>>,
                            ConnectError,
                        >,
                    > + Send,
            >,
        >,
    ) -> Result<(StreamInputResponse, Context), ConnectError> {
        use futures::StreamExt;

        let mut handle: Option<Arc<ProcessHandle>> = None;

        while let Some(result) = requests.next().await {
            let req = result?;
            match &req.event {
                Some(stream_input_request::EventView::Start(start)) => {
                    if let Some(selector) = start.process.as_option() {
                        handle = Some(self.get_process_by_selector(selector)?);
                    }
                }
                Some(stream_input_request::EventView::Data(data)) => {
                    let h = handle.as_ref().ok_or_else(|| {
                        ConnectError::new(ErrorCode::FailedPrecondition, "no start event received")
                    })?;
                    if let Some(input) = data.input.as_option() {
                        write_input(h, input).await?;
                    }
                }
                Some(stream_input_request::EventView::Keepalive(_)) => {}
                None => {}
            }
        }

        Ok((
            StreamInputResponse {
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn send_input(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<SendInputRequestView<'static>>,
    ) -> Result<(SendInputResponse, Context), ConnectError> {
        let selector = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process selector required")
        })?;
        let handle = self.get_process_by_selector(selector)?;

        if let Some(input) = request.input.as_option() {
            write_input(&handle, input).await?;
        }

        Ok((
            SendInputResponse {
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn send_signal(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<SendSignalRequestView<'static>>,
    ) -> Result<(SendSignalResponse, Context), ConnectError> {
        let selector = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process selector required")
        })?;
        let handle = self.get_process_by_selector(selector)?;

        let sig = match request.signal.as_known() {
            Some(Signal::SIGNAL_SIGKILL) => nix::sys::signal::Signal::SIGKILL,
            Some(Signal::SIGNAL_SIGTERM) => nix::sys::signal::Signal::SIGTERM,
            _ => {
                return Err(ConnectError::new(
                    ErrorCode::InvalidArgument,
                    "invalid or unspecified signal",
                ));
            }
        };

        handle.send_signal(sig)?;
        Ok((
            SendSignalResponse {
                ..Default::default()
            },
            ctx,
        ))
    }

    async fn close_stdin(
        &self,
        ctx: Context,
        request: buffa::view::OwnedView<CloseStdinRequestView<'static>>,
    ) -> Result<(CloseStdinResponse, Context), ConnectError> {
        let selector = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process selector required")
        })?;
        let handle = self.get_process_by_selector(selector)?;
        handle.close_stdin()?;
        Ok((
            CloseStdinResponse {
                ..Default::default()
            },
            ctx,
        ))
    }
}

// Input writes go straight to the fd, which is O_NONBLOCK (set at spawn):
// small interactive writes complete inline on the async worker, and a full
// buffer parks the task awaiting writability with a bounded deadline inside
// write_stdin / write_pty — no blocking-pool thread is ever pinned by a
// process that stopped reading its input.
async fn write_input(
    handle: &Arc<ProcessHandle>,
    input: &ProcessInputView<'_>,
) -> Result<(), ConnectError> {
    match &input.input {
        Some(process_input::InputView::Pty(d)) => handle.write_pty(d).await,
        Some(process_input::InputView::Stdin(d)) => handle.write_stdin(d).await,
        None => Ok(()),
    }
}

/// Shared event pump for `Start` and `Connect`. Yields a leading start event,
/// replays any buffered output (empty for `Start`), then forwards live output
/// and the final exit event. The caller wraps each `ProcessEvent` into its own
/// response envelope, so the streaming logic lives in exactly one place.
fn process_event_stream(
    pid: u32,
    replay: Vec<DataEvent>,
    mut data_rx: broadcast::Receiver<DataEvent>,
    mut end_rx: broadcast::Receiver<process_handler::EndEvent>,
    cached_end: Option<process_handler::EndEvent>,
) -> impl Stream<Item = Result<ProcessEvent, ConnectError>> {
    use broadcast::error::{RecvError, TryRecvError};

    async_stream::stream! {
        yield Ok(make_start_event(pid));

        for ev in replay {
            yield Ok(make_data_event(ev));
        }

        // Process already exited before we attached. The snapshot above covers
        // output up to the attach point; drain anything the live receiver
        // buffered after the snapshot, then emit the cached exit. end_rx may
        // never deliver here — a broadcast receiver only sees events sent after
        // it subscribed, and the exit can predate that — so cached_end is the
        // source of truth.
        if let Some(end) = cached_end {
            loop {
                match data_rx.try_recv() {
                    Ok(ev) => yield Ok(make_data_event(ev)),
                    Err(TryRecvError::Lagged(_)) => continue,
                    Err(_) => break,
                }
            }
            yield Ok(make_end_event(end));
            return;
        }

        loop {
            tokio::select! {
                biased;
                data = data_rx.recv() => {
                    match data {
                        Ok(ev) => yield Ok(make_data_event(ev)),
                        Err(RecvError::Lagged(_)) => continue,
                        Err(RecvError::Closed) => {
                            // Data channel closed: the process ended and its
                            // handle was dropped. The end event is published
                            // before the handle drop, so it is still buffered —
                            // emit it rather than losing the exit code.
                            if let Ok(end) = end_rx.try_recv() {
                                yield Ok(make_end_event(end));
                            }
                            break;
                        }
                    }
                }
                end = end_rx.recv() => {
                    // Process ended. The waiter joins the output readers before
                    // sending this event, so every byte is already in the data
                    // channel — drain it fully before the end.
                    loop {
                        match data_rx.try_recv() {
                            Ok(ev) => yield Ok(make_data_event(ev)),
                            Err(TryRecvError::Lagged(_)) => continue,
                            Err(_) => break,
                        }
                    }
                    if let Ok(end) = end {
                        yield Ok(make_end_event(end));
                    }
                    break;
                }
            }
        }
    }
}

fn wrap_start_response(event: ProcessEvent) -> StartResponse {
    StartResponse {
        event: buffa::MessageField::some(event),
        ..Default::default()
    }
}

fn wrap_connect_response(event: ProcessEvent) -> ConnectResponse {
    ConnectResponse {
        event: buffa::MessageField::some(event),
        ..Default::default()
    }
}

fn make_start_event(pid: u32) -> ProcessEvent {
    ProcessEvent {
        event: Some(process_event::Event::Start(Box::new(
            process_event::StartEvent {
                pid,
                ..Default::default()
            },
        ))),
        ..Default::default()
    }
}

fn make_data_event(ev: DataEvent) -> ProcessEvent {
    let output = match ev {
        DataEvent::Stdout(d) => Some(process_event::data_event::Output::Stdout(d.into())),
        DataEvent::Stderr(d) => Some(process_event::data_event::Output::Stderr(d.into())),
        DataEvent::Pty(d) => Some(process_event::data_event::Output::Pty(d.into())),
    };
    ProcessEvent {
        event: Some(process_event::Event::Data(Box::new(
            process_event::DataEvent {
                output,
                ..Default::default()
            },
        ))),
        ..Default::default()
    }
}

fn make_end_event(end: process_handler::EndEvent) -> ProcessEvent {
    ProcessEvent {
        event: Some(process_event::Event::End(Box::new(
            process_event::EndEvent {
                exit_code: end.exit_code,
                exited: end.exited,
                status: end.status,
                error: end.error,
                ..Default::default()
            },
        ))),
        ..Default::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cmd_expands_tilde_slash() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("~/bin/mytool", home_dir).unwrap();
        assert_eq!(result, "/home/testuser/bin/mytool");
    }

    #[test]
    fn cmd_expands_bare_tilde() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("~", home_dir).unwrap();
        assert_eq!(result, "/home/testuser");
    }

    #[test]
    fn cmd_passthrough_absolute() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("/usr/bin/env", home_dir).unwrap();
        assert_eq!(result, "/usr/bin/env");
    }

    #[test]
    fn cmd_passthrough_relative_no_tilde() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("bin/tool", home_dir).unwrap();
        assert_eq!(result, "bin/tool");
    }

    #[test]
    fn cmd_errors_on_other_user() {
        let home_dir = "/home/testuser";
        assert!(expand_tilde("~other/bin/tool", home_dir).is_err());
    }

    #[test]
    fn args_expands_tilde_slash() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("~/hi", home_dir).unwrap();
        assert_eq!(result, "/home/testuser/hi");
    }

    #[test]
    fn args_expands_bare_tilde() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("~", home_dir).unwrap();
        assert_eq!(result, "/home/testuser");
    }

    #[test]
    fn args_other_user_left_literal() {
        let home_dir = "/home/testuser";
        let args_raw = vec!["~other".to_string(), "~other/path".to_string()];
        let args: Vec<String> = args_raw
            .into_iter()
            .map(|a| expand_tilde(&a, home_dir).unwrap_or(a))
            .collect();
        assert_eq!(args, vec!["~other", "~other/path"]);
    }

    #[test]
    fn args_passthrough_absolute() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("/tmp/file", home_dir).unwrap();
        assert_eq!(result, "/tmp/file");
    }

    #[test]
    fn args_passthrough_relative_no_tilde() {
        let home_dir = "/home/testuser";
        let result = expand_tilde("relative/path", home_dir).unwrap();
        assert_eq!(result, "relative/path");
    }

    #[test]
    fn args_mixed_expands_tilde_keeps_rest() {
        let home_dir = "/home/testuser";
        let args_raw = vec![
            "-p".to_string(),
            "~/data".to_string(),
            "/tmp/out".to_string(),
            "~other".to_string(),
        ];
        let args: Vec<String> = args_raw
            .into_iter()
            .map(|a| expand_tilde(&a, home_dir).unwrap_or(a))
            .collect();
        assert_eq!(
            args,
            vec!["-p", "/home/testuser/data", "/tmp/out", "~other"]
        );
    }

    #[test]
    fn args_empty_passthrough() {
        let home_dir = "/home/testuser";
        let args_raw: Vec<String> = vec![];
        let args: Vec<String> = args_raw
            .into_iter()
            .map(|a| expand_tilde(&a, home_dir).unwrap_or(a))
            .collect();
        assert!(args.is_empty());
    }
}
