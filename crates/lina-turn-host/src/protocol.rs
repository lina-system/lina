use std::path::PathBuf;

use serde::Deserialize;
use serde_json::{Map, Value, json};
use tokio::sync::{mpsc, oneshot};

#[derive(Deserialize)]
#[serde(tag = "type")]
pub(crate) enum Command {
    #[serde(rename = "turn.start")]
    Start(Box<Start>),
    #[serde(rename = "interrupt", alias = "turn.cancel", alias = "cancel")]
    Interrupt {
        #[serde(flatten)]
        route: Route,
        #[serde(default)]
        turn_id: Option<String>,
    },
    #[serde(rename = "respond")]
    Respond {
        #[serde(flatten)]
        route: Route,
        #[serde(default)]
        params: Value,
    },
    #[serde(rename = "control", alias = "reload_mcp")]
    Control {
        #[serde(flatten)]
        route: Route,
    },
    #[serde(rename = "read_file.policy.ack")]
    ReadPolicyAck(Route),
    #[serde(rename = "shutdown")]
    Shutdown {
        #[serde(default)]
        request_id: Value,
    },
}

#[derive(Clone, Deserialize)]
pub(crate) struct Route {
    pub(crate) sid: String,
    #[serde(default)]
    pub(crate) request_id: Value,
}

impl Route {
    pub(crate) fn reply(&self, kind: &str, mut fields: Map<String, Value>) -> Value {
        fields.insert("type".into(), json!(kind));
        fields.insert("sid".into(), json!(self.sid));
        fields.insert("request_id".into(), self.request_id.clone());
        Value::Object(fields)
    }

    pub(crate) fn error(&self, reason: &str, message: &str) -> Value {
        self.reply(
            "turn.error",
            Map::from_iter([
                ("reason".into(), json!(reason)),
                ("message".into(), json!(message)),
            ]),
        )
    }
}

#[derive(Deserialize)]
pub(crate) struct Start {
    #[serde(flatten)]
    pub(crate) route: Route,
    pub(crate) turn_id: Option<String>,
    pub(crate) session_key: Option<String>,
    #[serde(default, alias = "prompt")]
    pub(crate) text: String,
    #[serde(default)]
    pub(crate) history: Vec<Map<String, Value>>,
    #[serde(default)]
    pub(crate) history_version: u64,
    pub(crate) cwd: PathBuf,
    pub(crate) profile_home: Option<PathBuf>,
    pub(crate) terminal_backend: Option<String>,
    #[serde(default)]
    pub(crate) attached_images: Vec<Value>,
    pub(crate) turn_plan: TurnPlan,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct TurnPlan {
    pub(crate) system: String,
    pub(crate) tools: Vec<Map<String, Value>>,
    pub(crate) model: String,
    pub(crate) base_url: String,
    pub(crate) params: Map<String, Value>,
    pub(crate) stream: bool,
}

pub(crate) enum Update {
    Frame(Value),
    ReadPolicy {
        route: Route,
        frame: Value,
        reply: oneshot::Sender<Value>,
    },
    Done {
        sid: String,
        frames: Vec<Value>,
        read_state: crate::read_file::ReadState,
    },
}

pub(crate) struct Events<'a> {
    pub(crate) sid: &'a str,
    pub(crate) turn_id: &'a str,
    pub(crate) segment: usize,
    pub(crate) offset: usize,
    first_output: bool,
    paragraph_after_tools: bool,
    pub(crate) request_active: bool,
    pub(crate) updates: &'a mpsc::Sender<Update>,
}

impl<'a> Events<'a> {
    pub(crate) fn new(sid: &'a str, turn_id: &'a str, updates: &'a mpsc::Sender<Update>) -> Self {
        Self {
            sid,
            turn_id,
            segment: 0,
            offset: 0,
            first_output: false,
            paragraph_after_tools: false,
            request_active: false,
            updates,
        }
    }

    pub(crate) fn frame(&self, kind: &str, payload: Value) -> Value {
        json!({
            "type": "rpc", "sid": self.sid, "message": {
                "jsonrpc": "2.0", "method": "event",
                "params": {"type": kind, "session_id": self.sid, "payload": payload}
            }
        })
    }

    pub(crate) async fn emit(&self, kind: &str, payload: Value) -> bool {
        self.updates
            .send(Update::Frame(self.frame(kind, payload)))
            .await
            .is_ok()
    }

    pub(crate) async fn begin_request(&mut self) -> bool {
        self.first_output = false;
        self.request_active = true;
        self.emit("thinking.delta", json!({"text": "Thinking..."}))
            .await
    }

    pub(crate) async fn first_output(&mut self) -> bool {
        if self.first_output {
            return true;
        }
        self.first_output = true;
        self.emit("thinking.delta", json!({"text": ""})).await
    }

    pub(crate) async fn end_request(&mut self) -> bool {
        self.request_active = false;
        self.emit("thinking.delta", json!({"text": ""})).await
    }

    pub(crate) async fn delta(&mut self, text: &str) -> bool {
        let with_break;
        let text = if self.paragraph_after_tools && !text.trim().is_empty() {
            self.paragraph_after_tools = false;
            with_break = format!("\n\n{text}");
            &with_break
        } else if self.offset == 0 && self.segment == 0 {
            text.trim_start_matches('\n')
        } else {
            text
        };
        if text.is_empty() {
            return true;
        }
        let emitted = self.emit("message.delta", json!({
            "text": text, "turn_id": self.turn_id, "segment": self.segment, "offset": self.offset
        })).await;
        self.offset += text.encode_utf16().count();
        emitted
    }

    pub(crate) fn after_tools(&mut self) {
        self.paragraph_after_tools = true;
    }

    pub(crate) async fn interim(&mut self, text: &str, streamed: bool) -> bool {
        let emitted = self
            .emit(
                "message.interim",
                json!({
                    "text": text, "already_streamed": streamed, "turn_id": self.turn_id,
                    "segment": self.segment
                }),
            )
            .await;
        self.offset = 0;
        self.segment += 1;
        emitted
    }
}

// Python's perf_counter_ns uses the same monotonic clock on Unix.
#[cfg(unix)]
pub(crate) fn now_ns() -> u64 {
    let ts = rustix::time::clock_gettime(rustix::time::ClockId::Monotonic);
    let secs = u64::try_from(ts.tv_sec).unwrap_or_default();
    let nanos = u64::try_from(ts.tv_nsec).unwrap_or_default();
    secs.saturating_mul(1_000_000_000).saturating_add(nanos)
}

#[cfg(not(unix))]
pub(crate) fn now_ns() -> u64 {
    static START: std::sync::OnceLock<std::time::Instant> = std::sync::OnceLock::new();
    u64::try_from(
        START
            .get_or_init(std::time::Instant::now)
            .elapsed()
            .as_nanos(),
    )
    .unwrap_or(u64::MAX)
}
