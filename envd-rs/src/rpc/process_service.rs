use std::collections::HashMap;
use std::pin::Pin;
use std::sync::Arc;

use connectrpc::{ConnectError, Context, ErrorCode};
use dashmap::DashMap;
use futures::Stream;

use crate::permissions::path::expand_and_resolve;
use crate::permissions::user::lookup_user;
use crate::rpc::pb::process::*;
use crate::rpc::process_handler::{self, DataEvent, ProcessHandle};
use crate::state::AppState;

pub struct ProcessServiceImpl {
    state: Arc<AppState>,
    processes: DashMap<u32, Arc<ProcessHandle>>,
}

impl ProcessServiceImpl {
    pub fn new(state: Arc<AppState>) -> Self {
        Self {
            state,
            processes: DashMap::new(),
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
    ) -> Result<Arc<ProcessHandle>, ConnectError> {
        let proc_config = request.process.as_option().ok_or_else(|| {
            ConnectError::new(ErrorCode::InvalidArgument, "process config required")
        })?;

        let username = self.state.defaults.user.clone();
        let user =
            lookup_user(&username).map_err(|e| ConnectError::new(ErrorCode::Internal, e))?;

        let cmd: &str = proc_config.cmd;
        let args: Vec<String> = proc_config.args.iter().map(|s| s.to_string()).collect();
        let envs: HashMap<String, String> = proc_config
            .envs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();

        let home_dir = user.dir.to_string_lossy().to_string();
        let cwd_str: &str = proc_config.cwd.unwrap_or("");
        let cwd = expand_and_resolve(cwd_str, &home_dir, self.state.defaults.workdir.as_deref())
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

        let handle = process_handler::spawn_process(
            cmd,
            &args,
            &envs,
            effective_cwd,
            pty_opts,
            enable_stdin,
            tag,
            &user,
            &self.state.defaults.env_vars,
        )?;

        self.processes.insert(handle.pid, Arc::clone(&handle));

        let processes = self.processes.clone();
        let pid = handle.pid;
        let mut end_rx = handle.subscribe_end();
        tokio::spawn(async move {
            let _ = end_rx.recv().await;
            processes.remove(&pid);
        });

        Ok(handle)
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
        let handle = self.spawn_from_request(&request)?;
        let pid = handle.pid;

        let mut data_rx = handle.subscribe_data();
        let mut end_rx = handle.subscribe_end();

        let stream = async_stream::stream! {
            yield Ok(make_start_response(pid));

            loop {
                match data_rx.recv().await {
                    Ok(ev) => yield Ok(make_data_start_response(ev)),
                    Err(tokio::sync::broadcast::error::RecvError::Lagged(_)) => continue,
                    Err(tokio::sync::broadcast::error::RecvError::Closed) => break,
                }
            }

            if let Ok(end) = end_rx.recv().await {
                yield Ok(make_end_start_response(end));
            }
        };

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

        let mut data_rx = handle.subscribe_data();
        let mut end_rx = handle.subscribe_end();

        let stream = async_stream::stream! {
            yield Ok(ConnectResponse {
                event: buffa::MessageField::some(ProcessEvent {
                    event: Some(process_event::Event::Start(Box::new(
                        process_event::StartEvent { pid, ..Default::default() },
                    ))),
                    ..Default::default()
                }),
                ..Default::default()
            });

            loop {
                match data_rx.recv().await {
                    Ok(ev) => {
                        yield Ok(ConnectResponse {
                            event: buffa::MessageField::some(make_data_event(ev)),
                            ..Default::default()
                        });
                    }
                    Err(tokio::sync::broadcast::error::RecvError::Lagged(_)) => continue,
                    Err(tokio::sync::broadcast::error::RecvError::Closed) => break,
                }
            }

            if let Ok(end) = end_rx.recv().await {
                yield Ok(ConnectResponse {
                    event: buffa::MessageField::some(make_end_event(end)),
                    ..Default::default()
                });
            }
        };

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

        Ok((UpdateResponse { ..Default::default() }, ctx))
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
                        write_input(h, input)?;
                    }
                }
                Some(stream_input_request::EventView::Keepalive(_)) => {}
                None => {}
            }
        }

        Ok((StreamInputResponse { ..Default::default() }, ctx))
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
            write_input(&handle, input)?;
        }

        Ok((SendInputResponse { ..Default::default() }, ctx))
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
                ))
            }
        };

        handle.send_signal(sig)?;
        Ok((SendSignalResponse { ..Default::default() }, ctx))
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
        Ok((CloseStdinResponse { ..Default::default() }, ctx))
    }
}

fn write_input(handle: &ProcessHandle, input: &ProcessInputView) -> Result<(), ConnectError> {
    match &input.input {
        Some(process_input::InputView::Pty(d)) => handle.write_pty(d),
        Some(process_input::InputView::Stdin(d)) => handle.write_stdin(d),
        None => Ok(()),
    }
}

fn make_start_response(pid: u32) -> StartResponse {
    StartResponse {
        event: buffa::MessageField::some(ProcessEvent {
            event: Some(process_event::Event::Start(Box::new(
                process_event::StartEvent {
                    pid,
                    ..Default::default()
                },
            ))),
            ..Default::default()
        }),
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

fn make_data_start_response(ev: DataEvent) -> StartResponse {
    StartResponse {
        event: buffa::MessageField::some(make_data_event(ev)),
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

fn make_end_start_response(end: process_handler::EndEvent) -> StartResponse {
    StartResponse {
        event: buffa::MessageField::some(make_end_event(end)),
        ..Default::default()
    }
}
