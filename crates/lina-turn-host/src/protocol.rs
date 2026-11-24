use std::path::PathBuf;

use serde::{Deserialize, Deserializer};
use serde_json::{Map, Value, json};
use tokio::sync::{mpsc, oneshot};

#[derive(Deserialize)]
#[serde(tag = "type")]
pub(crate) enum Command {
    #[serde(skip_deserializing)]
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
    #[serde(rename = "iteration.summary.ack")]
    IterationSummaryAck(Route),
    #[serde(rename = "context.preflight.ack")]
    ContextPreflightAck(Route),
    #[serde(rename = "model.retry.ack")]
    ModelRetryAck(Route),
    #[serde(rename = "shutdown")]
    Shutdown {
        #[serde(default)]
        request_id: Value,
    },
}

impl Command {
    pub(crate) fn from_frame(frame: Value) -> serde_json::Result<Self> {
        // An internally tagged enum buffers its fields in Serde's generic
        // Content, which cannot carry all of serde_json's integer types.
        if frame["type"] == "turn.start" {
            serde_json::from_value(frame).map(Self::Start)
        } else {
            serde_json::from_value(frame)
        }
    }
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
    #[serde(default = "single_attempt")]
    pub(crate) api_max_retries: usize,
    #[serde(default)]
    pub(crate) read_file_hints: bool,
    #[serde(
        default = "default_max_iterations",
        deserialize_with = "deserialize_max_iterations"
    )]
    pub(crate) max_iterations: Option<usize>,
}

fn default_max_iterations() -> Option<usize> {
    Some(500)
}

fn single_attempt() -> usize {
    1
}

fn deserialize_max_iterations<'de, D: Deserializer<'de>>(
    deserializer: D,
) -> Result<Option<usize>, D::Error> {
    let Value::Number(number) = Value::deserialize(deserializer)? else {
        return Ok(default_max_iterations());
    };
    if let Some(value) = number.as_u64() {
        return Ok(usize::try_from(value).ok().filter(|limit| *limit > 0));
    }
    if number.as_i64().is_some()
        || number
            .as_str()
            .trim_start_matches('-')
            .bytes()
            .all(|byte| byte.is_ascii_digit())
    {
        return Ok(None);
    }
    Ok(default_max_iterations())
}

#[cfg(test)]
mod tests {
    use super::{Command, Value};

    #[test]
    fn turn_plan_resolves_wire_iteration_limits() {
        let huge_integer = format!(r#", "max_iterations": 1{}"#, "0".repeat(400));
        let signed_max = format!(r#", "max_iterations": {}"#, i64::MAX);
        let unsigned_max = format!(r#", "max_iterations": {}"#, u64::MAX);
        for (field, expected) in [
            ("", Some(500)),
            (r#", "max_iterations": null"#, Some(500)),
            (r#", "max_iterations": "3""#, Some(500)),
            (r#", "max_iterations": 3.5"#, Some(500)),
            (r#", "max_iterations": 9223372036854775808.5"#, Some(500)),
            (r#", "max_iterations": 1e400"#, Some(500)),
            (r#", "max_iterations": 3"#, Some(3)),
            (r#", "max_iterations": 0"#, None),
            (r#", "max_iterations": -1"#, None),
            (signed_max.as_str(), usize::try_from(i64::MAX).ok()),
            (unsigned_max.as_str(), usize::try_from(u64::MAX).ok()),
            (r#", "max_iterations": 18446744073709551616000000000"#, None),
            (huge_integer.as_str(), None),
        ] {
            let wire = format!(
                r#"{{"type":"turn.start","sid":"test","cwd":"/","turn_plan":{{"system":"","tools":[],"model":"local","base_url":"","params":{{}},"stream":false{field}}}}}"#
            );
            let frame: Value = serde_json::from_str(&wire).unwrap();
            let Command::Start(start) = Command::from_frame(frame).unwrap() else {
                panic!("expected turn.start");
            };
            assert_eq!(start.turn_plan.max_iterations, expected, "{field}");
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum ParentRequestKind {
    ReadPolicy,
    IterationSummary,
    ContextPreflight,
    ModelRetry,
}

pub(crate) enum Update {
    Frame(Value),
    ParentRequest {
        kind: ParentRequestKind,
        route: Route,
        frame: Value,
        reply: oneshot::Sender<Value>,
    },
    Done {
        sid: String,
        frames: Vec<Value>,
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
