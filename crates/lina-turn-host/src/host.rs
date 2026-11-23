use std::collections::HashMap;
use std::io::{self, BufRead as _};
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use reqwest::Url;
use serde_json::{Map, Value, json};
use tokio::io::{AsyncWriteExt as _, Stdout};
use tokio::sync::{mpsc, oneshot};
use tokio::task::JoinSet;
use tokio::time::{Instant, MissedTickBehavior};
use uuid::Uuid;

use crate::model::ModelClient;
use crate::protocol::{Command, Route, Start, Update, now_ns};
use crate::read_file::ReadState;
use crate::turn::Worker;

struct Active {
    turn_id: Option<String>,
    cancel: Option<oneshot::Sender<()>>,
    policy: Option<Policy>,
    activity_ns: Option<u64>,
}

struct Policy {
    request_id: Value,
    reply: oneshot::Sender<Value>,
}

struct Host {
    home: Option<PathBuf>,
    endpoint: Option<Url>,
    worker: Worker,
    tasks: JoinSet<()>,
    active: HashMap<String, Active>,
    read_states: HashMap<String, ReadState>,
    progress: u64,
    stdout: Stdout,
}

pub(crate) async fn run() -> io::Result<()> {
    let (input, mut commands) = mpsc::channel(32);
    std::thread::Builder::new()
        .name("turn-host-stdin".into())
        .spawn(move || {
            for line in io::stdin().lock().lines() {
                let Ok(line) = line else { break };
                if input.blocking_send(line).is_err() {
                    break;
                }
            }
        })?;
    let (updates, mut output) = mpsc::channel(64);
    let home = std::env::var_os("HERMES_HOME").map(PathBuf::from);
    let secret = std::env::var("OPENAI_API_KEY")
        .ok()
        .filter(|value| !value.is_empty())
        .map(Arc::<str>::from);
    let model = Arc::new(ModelClient::new(secret.as_deref()).map_err(io::Error::other)?);
    let mut host = Host {
        home,
        endpoint: None,
        worker: Worker {
            model,
            secret,
            updates,
        },
        tasks: JoinSet::new(),
        active: HashMap::new(),
        read_states: HashMap::new(),
        progress: 0,
        stdout: tokio::io::stdout(),
    };
    host.write(json!({
        "type": "hello", "host_pid": std::process::id(), "boot_id": Uuid::new_v4().simple().to_string(),
        "build_sha": env!("LINA_TURN_HOST_BUILD_SHA"), "cwd": std::env::current_dir()?,
        "hermes_home": host.home.as_ref().map_or_else(String::new, |path| path.to_string_lossy().into_owned())
    })).await?;
    let interval = std::env::var("HERMES_COMPUTE_HOST_HEARTBEAT_SECS")
        .ok()
        .and_then(|value| value.parse::<f64>().ok())
        .and_then(|secs| Duration::try_from_secs_f64(secs).ok())
        .unwrap_or(Duration::from_secs(15));
    let period = interval.max(Duration::from_millis(1));
    let mut heartbeat = tokio::time::interval_at(Instant::now() + period, period);
    heartbeat.set_missed_tick_behavior(MissedTickBehavior::Skip);
    let second = Duration::from_secs(1);
    let mut activity = tokio::time::interval_at(Instant::now() + second, second);
    activity.set_missed_tick_behavior(MissedTickBehavior::Skip);
    let mut eof = false;
    let deadline = tokio::time::sleep(Duration::ZERO);
    tokio::pin!(deadline);
    loop {
        if eof && host.active.is_empty() {
            break;
        }
        tokio::select! {
            line = commands.recv(), if !eof => {
                match line {
                    Some(line) => if host.command(&line).await? { break; },
                    None => {
                        eof = true;
                        deadline.as_mut().reset(Instant::now() + Duration::from_secs(2));
                    }
                }
            }
            Some(update) = output.recv() => host.update(update).await?,
            Some(result) = host.tasks.join_next(), if !host.tasks.is_empty() => {
                result.map_err(io::Error::other)?;
            }
            _ = heartbeat.tick(), if !interval.is_zero() => host.heartbeat().await?,
            _ = activity.tick(), if !host.active.is_empty() => host.report_activity().await?,
            () = &mut deadline, if eof => break,
        }
    }
    Ok(())
}

impl Host {
    async fn write(&mut self, mut frame: Value) -> io::Result<()> {
        if frame["type"] != "read_file.policy" {
            frame["host_ns"] = json!(now_ns());
        }
        if let Some(secret) = self.worker.secret.as_deref() {
            redact(&mut frame, secret);
        }
        let mut line = serde_json::to_vec(&frame)?;
        line.push(b'\n');
        self.stdout.write_all(&line).await?;
        self.stdout.flush().await
    }

    async fn heartbeat(&mut self) -> io::Result<()> {
        self.write(json!({
            "type": "hb", "active_turns": self.active.len(), "progress_counter": self.progress,
            "rss_mb": 0.0
        }))
        .await
    }

    async fn report_activity(&mut self) -> io::Result<()> {
        let frames: Vec<_> = self.active.iter().map(|(sid, turn)| json!({
            "type": "rpc", "sid": "", "message": {
                "jsonrpc": "2.0", "method": "compute_host.activity", "params": {
                    "session_id": sid, "turn_id": turn.turn_id, "activity_ns": turn.activity_ns
                }
            }
        })).collect();
        for frame in frames {
            self.write(frame).await?;
        }
        Ok(())
    }

    async fn command(&mut self, line: &str) -> io::Result<bool> {
        let Ok(frame) = serde_json::from_str::<Value>(line) else {
            self.write(json!({"type": "error", "message": "invalid json"}))
                .await?;
            return Ok(false);
        };
        let command = match serde_json::from_value(frame.clone()) {
            Ok(command) => command,
            Err(_) => {
                let invalid = if frame["type"] == "turn.start" {
                    json!({"type": "turn.error", "sid": frame["sid"], "request_id": frame["request_id"],
                           "reason": "ineligible_turn", "message": "invalid or missing turn plan"})
                } else {
                    json!({"type": "error", "request_id": frame["request_id"], "message": "invalid control frame"})
                };
                self.write(invalid).await?;
                return Ok(false);
            }
        };
        match command {
            Command::Start(start) => self.start(*start).await?,
            Command::Interrupt { route, turn_id } => {
                self.cancel(&route, turn_id.as_deref()).await?
            }
            Command::Respond { route, params } => {
                let failure = if !self.active.contains_key(&route.sid)
                    && !self.read_states.contains_key(&route.sid)
                {
                    Some("session not found")
                } else if !params.is_object() {
                    Some("response params must be an object")
                } else {
                    None
                };
                let response = if let Some(message) = failure {
                    route.reply(
                        "respond.error",
                        Map::from_iter([("message".into(), json!(message))]),
                    )
                } else {
                    route.reply("respond.ack", Map::from_iter([("response".into(), json!({
                        "jsonrpc": "2.0", "id": route.request_id, "result": {"status": "expired"}
                    }))]))
                };
                self.write(response).await?;
            }
            Command::Control { route } => {
                self.write(route.reply(
                    "control.error",
                    Map::from_iter([(
                        "message".into(),
                        json!("control is unavailable on the Rust turn host"),
                    )]),
                ))
                .await?;
            }
            Command::ReadPolicyAck(route) => {
                if let Some(turn) = self.active.get_mut(&route.sid)
                    && frame["turn_id"].as_str() == turn.turn_id.as_deref()
                    && turn
                        .policy
                        .as_ref()
                        .is_some_and(|request| request.request_id == route.request_id)
                    && let Some(policy) = turn.policy.take()
                {
                    drop(policy.reply.send(frame));
                }
            }
            Command::Shutdown { request_id } => {
                self.write(json!({"type": "shutdown.ack", "request_id": request_id}))
                    .await?;
                return Ok(true);
            }
        }
        Ok(false)
    }

    async fn start(&mut self, mut start: Start) -> io::Result<()> {
        let route = &start.route;
        let eligible = !route.sid.is_empty()
            && start.cwd.is_absolute()
            && start.attached_images.is_empty()
            && start.terminal_backend.as_deref() == Some("local")
            && !start.turn_plan.model.is_empty()
            && start
                .profile_home
                .as_ref()
                .filter(|home| !home.as_os_str().is_empty())
                .is_none_or(|home| Some(home) == self.home.as_ref());
        if !eligible {
            return self
                .write(route.error(
                    "ineligible_turn",
                    "turn requires the scoped profile and a local text session",
                ))
                .await;
        }
        if self.active.contains_key(&route.sid) {
            return self
                .write(route.error("session_busy", "session busy"))
                .await;
        }
        let endpoint = match self.worker.model.endpoint(&start.turn_plan.base_url) {
            Ok(endpoint) => endpoint,
            Err(err) => {
                return self
                    .write(route.error("model_endpoint", &err.to_string()))
                    .await;
            }
        };
        if self
            .endpoint
            .as_ref()
            .is_some_and(|bound| bound != &endpoint)
        {
            return self
                .write(route.error(
                    "model_endpoint",
                    "host is bound to a different model endpoint",
                ))
                .await;
        }
        self.endpoint = Some(endpoint.clone());
        start
            .turn_id
            .get_or_insert_with(|| Uuid::new_v4().simple().to_string());
        let (cancel, cancelled) = oneshot::channel();
        self.active.insert(
            route.sid.clone(),
            Active {
                turn_id: start.turn_id.clone(),
                cancel: Some(cancel),
                policy: None,
                activity_ns: None,
            },
        );
        self.write(route.reply(
            "turn.started",
            Map::from_iter([("started_ns".into(), json!(now_ns()))]),
        ))
        .await?;
        let worker = Worker {
            model: Arc::clone(&self.worker.model),
            secret: self.worker.secret.clone(),
            updates: self.worker.updates.clone(),
        };
        let state = self.read_states.remove(&route.sid).unwrap_or_default();
        self.tasks
            .spawn(worker.run(start, endpoint, (cancelled, state)));
        Ok(())
    }

    async fn cancel(&mut self, route: &Route, turn_id: Option<&str>) -> io::Result<()> {
        let applied = self
            .active
            .get_mut(&route.sid)
            .filter(|turn| turn_id.is_none() || turn.turn_id.as_deref() == turn_id)
            .and_then(|turn| turn.cancel.take())
            .is_some_and(|cancel| cancel.send(()).is_ok());
        let mut fields = Map::from_iter([("applied".into(), json!(applied))]);
        if applied {
            fields.insert("applied_ns".into(), json!(now_ns()));
        }
        self.write(route.reply("interrupt.ack", fields)).await
    }

    async fn update(&mut self, update: Update) -> io::Result<()> {
        match update {
            Update::Frame(frame) => {
                let event = frame["message"]["params"]["type"].as_str();
                if event != Some("message.start")
                    && let Some(sid) = frame["sid"].as_str()
                    && let Some(turn) = self.active.get_mut(sid)
                {
                    turn.activity_ns = Some(now_ns());
                }
                self.write(frame).await
            }
            Update::ReadPolicy {
                route,
                frame,
                reply,
            } => {
                if let Some(turn) = self.active.get_mut(&route.sid)
                    && (turn.cancel.is_some() || frame["op"] == "redact")
                {
                    turn.policy = Some(Policy {
                        request_id: route.request_id,
                        reply,
                    });
                    self.write(frame).await?;
                }
                Ok(())
            }
            Update::Done {
                sid,
                frames,
                read_state,
            } => {
                self.active.remove(&sid);
                self.read_states.insert(sid, read_state);
                self.progress = self.progress.saturating_add(1);
                for frame in frames {
                    self.write(frame).await?;
                }
                Ok(())
            }
        }
    }
}

fn redact(value: &mut Value, secret: &str) {
    match value {
        Value::String(text) if text.contains(secret) => *text = text.replace(secret, "[REDACTED]"),
        Value::Array(items) => items.iter_mut().for_each(|item| redact(item, secret)),
        Value::Object(fields) => fields.values_mut().for_each(|item| redact(item, secret)),
        _ => {}
    }
}
