use std::collections::VecDeque;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use reqwest::Url;
use serde_json::{Map, Value, json};
use tokio::sync::{mpsc, oneshot};
use uuid::Uuid;

use crate::model::{self, Message, ModelClient, ModelError, RequestMode, ToolCall, TurnUsage};
use crate::protocol::{Events, Route, Start, Update, now_ns};
use crate::read_file::ReadState;

const MAX_MODEL_CALLS: usize = 90;

pub(crate) struct Worker {
    pub(crate) model: Arc<ModelClient>,
    pub(crate) secret: Option<Arc<str>>,
    pub(crate) updates: mpsc::Sender<Update>,
}

#[derive(Debug, thiserror::Error)]
enum TurnError {
    #[error(transparent)]
    Model(#[from] ModelError),
    #[error("the model requested an unsupported tool")]
    UnsupportedTool,
    #[error("the turn reached its model call limit")]
    CallLimit,
    #[error("the turn was interrupted")]
    Interrupted,
}

#[derive(Default)]
struct Transcript {
    messages: Vec<Map<String, Value>>,
    partial: String,
    usage: TurnUsage,
    pending_tools: VecDeque<PendingTool>,
    reading_tool: bool,
}

struct PendingTool {
    id: String,
    args: Value,
    valid_args: bool,
    started_at: Instant,
}

impl PendingTool {
    fn new(call: &ToolCall) -> Self {
        let args = serde_json::from_str::<Map<String, Value>>(&call.function.arguments);
        let (args, valid_args) = match args {
            Ok(args) => (Value::Object(args), true),
            Err(_) => (json!({}), false),
        };
        Self {
            id: call.id.clone(),
            args,
            valid_args,
            started_at: Instant::now(),
        }
    }

    async fn announce(&self, events: &Events<'_>) -> bool {
        let mut start =
            json!({"tool_id": self.id, "name": "read_file", "context": read_preview(&self.args)});
        if self.args.as_object().is_some_and(|args| !args.is_empty()) {
            start["args"] = self.args.clone();
        }
        events.emit("tool.start", start).await
    }

    fn complete(self, result: &Value, messages: &mut Vec<Map<String, Value>>) -> Value {
        let content = if let Some(text) = result.as_str() {
            text.to_owned()
        } else {
            python_json(result)
        };
        let mut row = message("tool", json!(content));
        row.insert("tool_name".into(), json!("read_file"));
        row.insert("tool_call_id".into(), json!(self.id));
        messages.push(row);
        json!({
            "tool_id": self.id, "name": "read_file", "args": self.args,
            "duration_s": self.started_at.elapsed().as_secs_f64(), "result": result
        })
    }
}

impl Transcript {
    async fn close_interrupted(&mut self, events: &Events<'_>) -> bool {
        while let Some(tool) = self.pending_tools.pop_front() {
            let result = if std::mem::take(&mut self.reading_tool) {
                "[Tool execution cancelled — read_file was abandoned: interrupt requested]"
            } else {
                "[Tool execution skipped — read_file was not started. Turn interrupted]"
            };
            let payload = tool.complete(&json!(result), &mut self.messages);
            if !events.emit("tool.complete", payload).await {
                return false;
            }
        }
        if !self.partial.is_empty()
            || self
                .messages
                .last()
                .is_some_and(|row| row["role"] == "tool")
        {
            self.messages
                .push(message("assistant", json!(self.partial.trim())));
        }
        true
    }
}

impl Worker {
    pub(crate) async fn run(
        self,
        mut start: Start,
        endpoint: Url,
        (mut cancel, mut read_state): (oneshot::Receiver<()>, ReadState),
    ) {
        let turn_id = start
            .turn_id
            .clone()
            .unwrap_or_else(|| Uuid::new_v4().simple().to_string());
        let sid = &start.route.sid;
        let mut events = Events::new(sid, &turn_id, &self.updates);
        let mut transcript = Transcript {
            messages: std::mem::take(&mut start.history),
            ..Transcript::default()
        };
        let history_len = transcript.messages.len();
        transcript.messages.push(message("user", json!(start.text)));
        if !events
            .emit("message.start", json!({"turn_id": turn_id}))
            .await
        {
            return;
        }
        let context = TurnContext {
            start: &start,
            endpoint: &endpoint,
            worker: &self,
        };
        let result = match context
            .run_model(&mut transcript, &mut events, &mut read_state, &mut cancel)
            .await
        {
            Err(TurnError::Interrupted) => None,
            result => Some(result),
        };
        if events.request_active && !events.end_request().await {
            return;
        }
        if let Some(Err(TurnError::UnsupportedTool)) = result {
            let frame = start.route.error(
                "unsupported_tool",
                "the model requested an unsupported tool",
            );
            self.finish(sid, vec![frame], read_state).await;
            return;
        }
        let provider_error = match &result {
            Some(Err(TurnError::Model(ModelError::Http { status, summary })))
                if (400..600).contains(status) =>
            {
                Some(json!({"status_code": status, "message": summary, "attempts": 1}))
            }
            _ => None,
        };
        let usage = transcript.usage.payload(&start.turn_plan.model);
        let (status, mut completion) = match result {
            Some(Ok(answer)) => {
                let mut payload = json!({"text": answer.content.unwrap_or_default()});
                if let Some(reasoning) = answer.reasoning_content {
                    payload["reasoning"] = json!(reasoning);
                }
                ("complete", payload)
            }
            Some(Err(err)) => (
                "error",
                json!({"text": err.to_string(), "error": err.to_string(), "recoverable": false}),
            ),
            None => {
                if !transcript.close_interrupted(&events).await {
                    return;
                }
                ("interrupted", json!({"text": transcript.partial}))
            }
        };
        completion["usage"] = usage;
        completion["status"] = json!(status);
        let mut end = Map::from_iter([
            (
                "session_key".into(),
                json!(start.session_key.as_deref().unwrap_or(sid)),
            ),
            ("turn_id".into(), json!(turn_id)),
            (
                "history_version".into(),
                json!(start.history_version.saturating_add(1)),
            ),
            ("message_count".into(), json!(transcript.messages.len())),
            (
                "new_messages".into(),
                json!(&transcript.messages[history_len..]),
            ),
            ("messages".into(), json!(transcript.messages)),
            ("interrupted".into(), json!(status == "interrupted")),
            ("ended_ns".into(), json!(now_ns())),
        ]);
        if let Some(provider_error) = provider_error {
            end.insert("provider_error".into(), provider_error);
        }
        let frames = vec![
            events.frame("message.complete", completion),
            start.route.reply("turn.end", end),
        ];
        self.finish(sid, frames, read_state).await;
    }

    async fn finish(&self, sid: &str, frames: Vec<Value>, read_state: ReadState) {
        if self
            .updates
            .send(Update::Done {
                sid: sid.to_owned(),
                frames,
                read_state,
            })
            .await
            .is_err()
        {
            // The transport has shut down.
        }
    }
}

struct TurnContext<'a> {
    start: &'a Start,
    endpoint: &'a Url,
    worker: &'a Worker,
}

impl TurnContext<'_> {
    async fn request_round(
        &self,
        transcript: &mut Transcript,
        events: &mut Events<'_>,
    ) -> Result<Message, ModelError> {
        let plan = &self.start.turn_plan;
        if !events.begin_request().await {
            return Err(ModelError::OutputClosed);
        }
        let now = Instant::now();
        let mode = if plan.stream {
            RequestMode::Stream
        } else {
            RequestMode::Json
        };
        let model = &self.worker.model;
        let secret = self.worker.secret.as_deref();
        let response = model
            .request(self.endpoint, plan, &transcript.messages, mode, secret)
            .await;
        let (answer, usage) = match response {
            Ok(response) if plan.stream => {
                model::stream(
                    response,
                    events,
                    &mut transcript.partial,
                    self.worker.secret.as_deref(),
                )
                .await?
            }
            Ok(response) => model::nonstream(response).await?,
            Err(original @ ModelError::Http { status, .. })
                if plan.stream && (500..600).contains(&status) && model.claim_stream_probe() =>
            {
                match model
                    .request(
                        self.endpoint,
                        plan,
                        &transcript.messages,
                        RequestMode::Probe,
                        secret,
                    )
                    .await
                {
                    Ok(response) => {
                        let (answer, usage) =
                            model::nonstream(response).await.map_err(|_| original)?;
                        if let Some(text) = &answer.content
                            && !text.is_empty()
                        {
                            if !events.first_output().await || !events.delta(text).await {
                                return Err(ModelError::OutputClosed);
                            }
                            transcript.partial.push_str(text);
                        }
                        (answer, usage)
                    }
                    Err(probe @ ModelError::Http { status, .. })
                        if (400..500).contains(&status) =>
                    {
                        return Err(probe);
                    }
                    Err(_) => return Err(original),
                }
            }
            Err(error) => return Err(error),
        };
        let elapsed = now.elapsed();
        transcript.usage.calls += 1;
        if !events.end_request().await {
            return Err(ModelError::OutputClosed);
        }
        if let Some(usage) = usage {
            transcript.usage.add(usage, elapsed);
        }
        Ok(answer)
    }

    async fn run_model(
        &self,
        transcript: &mut Transcript,
        events: &mut Events<'_>,
        state: &mut ReadState,
        cancel: &mut oneshot::Receiver<()>,
    ) -> Result<Message, TurnError> {
        let plan = &self.start.turn_plan;
        for _ in 0..MAX_MODEL_CALLS {
            let answer = tokio::select! {
                biased;
                _ = &mut *cancel => return Err(TurnError::Interrupted),
                answer = self.request_round(transcript, events) => answer?,
            };
            if answer
                .tool_calls
                .iter()
                .any(|call| call.function.name != "read_file")
            {
                return Err(TurnError::UnsupportedTool);
            }
            if answer.tool_calls.iter().any(|call| !call.valid_function()) {
                return Err(ModelError::InvalidResponse.into());
            }
            let mut assistant = message(
                "assistant",
                json!(answer.content.as_deref().unwrap_or_default().trim()),
            );
            if let Some(reason) = &answer.finish_reason {
                assistant.insert("finish_reason".into(), json!(reason));
            }
            if let Some(reasoning) = &answer.reasoning_content {
                assistant.insert("reasoning".into(), json!(reasoning));
                assistant.insert("reasoning_content".into(), json!(reasoning));
            }
            if answer.tool_calls.is_empty() {
                transcript.messages.push(assistant);
                return Ok(answer);
            }
            if let Some(text) = &answer.content
                && !text.is_empty()
                && !events.interim(text, plan.stream).await
            {
                return Err(ModelError::OutputClosed.into());
            }
            transcript.partial.clear();
            assistant.insert("tool_calls".into(), json!(answer.tool_calls));
            transcript.messages.push(assistant);
            for call in &answer.tool_calls {
                transcript.pending_tools.push_back(PendingTool::new(call));
            }
            for call in &transcript.pending_tools {
                if !call.announce(events).await {
                    return Err(ModelError::OutputClosed.into());
                }
            }
            while let Some(call) = transcript.pending_tools.front() {
                transcript.reading_tool = true;
                let (result, interrupted) =
                    self.read_tool(call, state, events.turn_id, cancel).await?;
                let Some(call) = transcript.pending_tools.pop_front() else {
                    break;
                };
                transcript.reading_tool = false;
                let payload = call.complete(&result, &mut transcript.messages);
                if !events.emit("tool.complete", payload).await {
                    return Err(ModelError::OutputClosed.into());
                }
                if interrupted {
                    return Err(TurnError::Interrupted);
                }
            }
            events.after_tools();
        }
        Err(TurnError::CallLimit)
    }

    async fn read_tool(
        &self,
        call: &PendingTool,
        state: &mut ReadState,
        turn_id: &str,
        cancel: &mut oneshot::Receiver<()>,
    ) -> Result<(Value, bool), TurnError> {
        if !call.valid_args {
            return Ok((
                json!({"error": "read_file arguments must be a JSON object"}),
                false,
            ));
        }
        let authorized = AtomicBool::new(false);
        let read = state.read(&call.args, |payload| {
            self.read_policy(turn_id, payload, &authorized)
        });
        tokio::pin!(read);
        tokio::select! {
            biased;
            _ = cancel => {
                if !authorized.load(Ordering::Relaxed) {
                    return Err(TurnError::Interrupted);
                }
                // Only the already authorized read can finish during the worker grace.
                tokio::time::timeout(Duration::from_secs(3), read).await
                    .map(|result| (result, true)).map_err(|_| TurnError::Interrupted)
            }
            result = &mut read => Ok((result, false)),
        }
    }

    async fn read_policy(
        &self,
        turn_id: &str,
        mut payload: Value,
        authorized: &AtomicBool,
    ) -> Option<Value> {
        let is_authorize = payload["op"] == "authorize";
        let route = Route {
            sid: self.start.route.sid.clone(),
            request_id: json!(Uuid::new_v4().simple().to_string()),
        };
        let fields = payload.as_object_mut()?;
        fields.insert("type".into(), json!("read_file.policy"));
        fields.insert("sid".into(), json!(route.sid));
        fields.insert("request_id".into(), route.request_id.clone());
        fields.insert("turn_id".into(), json!(turn_id));
        let (sender, receiver) = oneshot::channel();
        self.worker
            .updates
            .send(Update::ReadPolicy {
                route,
                frame: payload,
                reply: sender,
            })
            .await
            .ok()?;
        let ack = tokio::time::timeout(Duration::from_secs(5), receiver)
            .await
            .ok()?
            .ok()?;
        if is_authorize && ack["ok"] == true {
            authorized.store(true, Ordering::Relaxed);
        }
        Some(ack)
    }
}

fn message(role: &str, content: Value) -> Map<String, Value> {
    Map::from_iter([("role".into(), json!(role)), ("content".into(), content)])
}

fn read_preview(args: &Value) -> String {
    let Some(path) = ["path", "file", "filepath"]
        .into_iter()
        .find_map(|name| args[name].as_str().filter(|path| !path.is_empty()))
    else {
        return String::new();
    };
    let label = path
        .trim_end_matches(['/', '\\'])
        .rsplit(['/', '\\'])
        .next()
        .filter(|name| !name.is_empty())
        .unwrap_or(path);
    let mut context = label.to_owned();
    if let Some(offset) = args["offset"].as_u64().filter(|offset| *offset > 0) {
        context.push_str(&format!(" L{offset}"));
        if let Some(limit) = args["limit"].as_u64().filter(|limit| *limit > 1) {
            context.push_str(&format!("-{}", offset.saturating_add(limit - 1)));
        }
    }
    if context.chars().count() > 80 {
        context = context.chars().take(77).collect();
        context.push_str("...");
    }
    context
}

fn python_json(value: &Value) -> String {
    use serde::Serialize as _;
    use serde_json::ser::{Formatter, Serializer};
    use std::io::{self, Write};

    struct Spaced {
        ensure_ascii: bool,
    }
    impl Formatter for Spaced {
        fn write_string_fragment<W: ?Sized + Write>(
            &mut self,
            writer: &mut W,
            fragment: &str,
        ) -> io::Result<()> {
            if !self.ensure_ascii {
                return writer.write_all(fragment.as_bytes());
            }
            for unit in fragment.encode_utf16() {
                if unit <= 0x7e {
                    writer.write_all(&[unit as u8])?;
                } else {
                    write!(writer, "\\u{unit:04x}")?;
                }
            }
            Ok(())
        }

        fn begin_array_value<W: ?Sized + Write>(
            &mut self,
            writer: &mut W,
            first: bool,
        ) -> io::Result<()> {
            writer.write_all(if first { b"" } else { b", " })
        }
        fn begin_object_key<W: ?Sized + Write>(
            &mut self,
            writer: &mut W,
            first: bool,
        ) -> io::Result<()> {
            self.begin_array_value(writer, first)
        }
        fn begin_object_value<W: ?Sized + Write>(&mut self, writer: &mut W) -> io::Result<()> {
            writer.write_all(b": ")
        }
    }
    let mut output = Vec::new();
    // Python's special-file branch uses json.dumps' default ensure_ascii=True.
    let format = Spaced {
        ensure_ascii: value["success"] == false && value["note"].is_string(),
    };
    if value
        .serialize(&mut Serializer::with_formatter(&mut output, format))
        .is_err()
    {
        return value.to_string();
    }
    String::from_utf8(output).unwrap_or_else(|_| value.to_string())
}
