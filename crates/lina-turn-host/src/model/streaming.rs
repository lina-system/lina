use std::collections::BTreeMap;

use eventsource_stream::Eventsource as _;
use futures_util::StreamExt as _;
use reqwest::{Response, header::HeaderMap};
use serde_json::{Value, json};

use super::{
    CONTENT_FILTER, Completion, MAX_ERROR_BYTES, Message, ModelError, ToolCall, ToolDelta, Usage,
    provider_error,
};
use crate::protocol::Events;

const THINK_TAGS: [(&str, &str); 9] = [
    ("<think>", "</think>"),
    ("<thinking>", "</thinking>"),
    ("<reasoning>", "</reasoning>"),
    ("<thought>", "</thought>"),
    ("<reasoning_scratchpad>", "</reasoning_scratchpad>"),
    ("<思考>", "</思考>"),
    ("<反思>", "</反思>"),
    ("<推理>", "</推理>"),
    ("<推敲>", "</推敲>"),
];

#[derive(Default)]
struct VisibleText {
    pending: String,
    closing: Option<&'static str>,
    saw_reasoning: bool,
    line_has_text: bool,
}

impl VisibleText {
    fn push(&mut self, text: &str) -> String {
        self.pending.push_str(text);
        self.drain(false, true)
    }

    fn drain(&mut self, finished: bool, keep_orphan_open: bool) -> String {
        let mut visible = String::new();
        while let Some(start) = self.pending.find('<') {
            if self.closing.is_none() {
                let prefix = &self.pending[..start];
                if let Some((_, last_line)) = prefix.rsplit_once('\n') {
                    self.line_has_text = !last_line.trim().is_empty();
                } else {
                    self.line_has_text |= !prefix.trim().is_empty();
                }
                visible.push_str(prefix);
            }
            self.pending.drain(..start);
            let lowered = self.pending.to_ascii_lowercase();
            if let Some(closing) = self.closing {
                if lowered.starts_with(closing) {
                    self.pending.drain(..closing.len());
                    self.closing = None;
                } else if !finished && closing.starts_with(&lowered) {
                    return visible;
                } else {
                    self.pending.drain(..1);
                }
            } else if let Some((open, close)) = THINK_TAGS
                .iter()
                .find(|(open, _)| lowered.starts_with(open))
            {
                if self.line_has_text && !lowered[open.len()..].contains(close) {
                    if !finished {
                        return visible;
                    }
                    let end = if keep_orphan_open {
                        visible.push_str(&self.pending[..open.len()]);
                        open.len()
                    } else {
                        self.pending.len() - self.pending[open.len()..].trim_start().len()
                    };
                    self.pending.drain(..end);
                    continue;
                }
                self.pending.drain(..open.len());
                self.closing = Some(close);
                self.saw_reasoning |= matches!(*open, "<think>" | "<thinking>" | "<reasoning>");
            } else if let Some((_, close)) = THINK_TAGS
                .iter()
                .find(|(_, close)| lowered.starts_with(close))
            {
                self.pending.drain(..close.len());
            } else if !finished
                && THINK_TAGS
                    .iter()
                    .any(|(open, close)| open.starts_with(&lowered) || close.starts_with(&lowered))
            {
                return visible;
            } else {
                visible.push('<');
                self.line_has_text = true;
                self.pending.drain(..1);
            }
        }
        if self.closing.is_none() {
            if let Some((_, last_line)) = self.pending.rsplit_once('\n') {
                self.line_has_text = !last_line.trim().is_empty();
            } else {
                self.line_has_text |= !self.pending.trim().is_empty();
            }
            visible.push_str(&self.pending);
        }
        self.pending.clear();
        visible
    }

    fn finish(mut self, keep_orphan_open: bool) -> (String, bool) {
        let visible = self.drain(true, keep_orphan_open);
        (visible, self.saw_reasoning)
    }
}

pub(crate) fn visible_text(text: &str) -> (String, bool) {
    let mut scrubber = VisibleText::default();
    let mut visible = scrubber.push(text);
    let (tail, saw_reasoning) = scrubber.finish(false);
    visible.push_str(&tail);
    (visible, saw_reasoning)
}

pub(crate) fn inline_reasoning(text: &str) -> Option<String> {
    let lowered = text.to_ascii_lowercase();
    let mut parts = Vec::new();
    for (open, close) in THINK_TAGS {
        let mut offset = 0;
        while let Some(start) = lowered[offset..].find(open) {
            let start = offset + start + open.len();
            let Some(end) = lowered[start..].find(close) else {
                break;
            };
            let end = start + end;
            let block = text[start..end].trim();
            if !block.is_empty() && !parts.contains(&block) {
                parts.push(block);
            }
            offset = end + close.len();
        }
    }
    (!parts.is_empty()).then(|| parts.join("\n\n"))
}

fn stream_error(data: &str, headers: &HeaderMap, secret: Option<&str>) -> ModelError {
    // Inspect the bounded error envelope only on the failure path. Normal token frames
    // deserialize directly into Completion without allocating a JSON object tree.
    if data.len() > MAX_ERROR_BYTES {
        return ModelError::InvalidResponse;
    }
    let Ok(body) = serde_json::from_str::<Value>(data) else {
        return ModelError::InvalidResponse;
    };
    let error = &body["error"];
    // Same numeric statuses as agent.error_classifier._status_code_from_body.
    let status = [
        &error["status_code"],
        &error["status"],
        &error["http_status"],
        &error["code"],
        &body["code"],
    ]
    .into_iter()
    .filter_map(Value::as_u64)
    .find(|n| (400..600).contains(n));
    match status.and_then(|n| u16::try_from(n).ok()) {
        Some(status) => provider_error(status, &body, headers, secret),
        None => ModelError::InvalidResponse,
    }
}

pub(crate) async fn stream(
    mut response: Response,
    events: &mut Events<'_>,
    partial: &mut String,
    secret: Option<&str>,
) -> Result<(Message, Option<Usage>), ModelError> {
    let headers = std::mem::take(response.headers_mut());
    let mut stream = response.bytes_stream().eventsource();
    let mut calls = BTreeMap::new();
    let mut usage = None;
    let mut finished = false;
    let mut finish_reason = None;
    let mut text = StreamText::default();
    let mut refusal = String::new();
    let mut scrubber = VisibleText::default();
    let mut reasoning = StreamText::default();
    let mut reasoning_details = Vec::new();
    while let Some(event) = stream.next().await {
        // Role and usage frames carry no model output. Buffered text, reasoning,
        // refusal, tool fragments and a finish reason all preclude replay.
        let replay_safe = || {
            !finished
                && text.is_empty()
                && reasoning.is_empty()
                && refusal.is_empty()
                && reasoning_details.is_empty()
                && calls.is_empty()
        };
        let event = event.map_err(|error| match error {
            eventsource_stream::EventStreamError::Transport(err) if replay_safe() => {
                ModelError::from_transport(err)
            }
            eventsource_stream::EventStreamError::Transport(_) => ModelError::IncompleteStream,
            _ => ModelError::InvalidResponse,
        })?;
        if event.data.trim() == "[DONE]" {
            finished = true;
            break;
        }
        let chunk: Completion =
            serde_json::from_str(&event.data).map_err(|_| ModelError::InvalidResponse)?;
        if chunk.error.is_some() {
            return Err(if replay_safe() {
                stream_error(&event.data, &headers, secret)
            } else {
                ModelError::IncompleteStream
            });
        }
        if let Some(sample) = chunk.usage {
            usage = Some(sample);
        }
        let Some(choice) = chunk.choices.into_iter().next() else {
            continue;
        };
        if let Some(reason) = choice.finish_reason {
            finished = true;
            if finish_reason.as_deref() != Some(CONTENT_FILTER) {
                finish_reason = Some(reason);
            }
        }
        if let Some(delta) = choice.delta {
            if let Some(part) = delta.refusal {
                refusal.push_str(&part);
            }
            if let Some(content) = delta.content {
                let visible = scrubber.push(&text.push(&content, secret));
                if !visible.is_empty() {
                    partial.push_str(&visible);
                    if !events.first_output().await || !events.delta(&visible).await {
                        return Err(ModelError::OutputClosed);
                    }
                }
            }
            if let Some(content) = delta.reasoning_content.or(delta.reasoning) {
                let visible = reasoning.push(&content, secret);
                if !visible.is_empty()
                    && (!events.first_output().await
                        || !events
                            .emit("reasoning.delta", json!({"text": visible}))
                            .await)
                {
                    return Err(ModelError::OutputClosed);
                }
            }
            reasoning_details.extend(delta.reasoning_details.into_iter().flatten());
            for delta in delta.tool_calls.into_iter().flatten() {
                let call = calls.entry(delta.index).or_insert_with(ToolCall::default);
                let previously_named = !call.function.name.is_empty();
                append_tool_delta(call, delta);
                let name = &call.function.name;
                if !previously_named
                    && !name.is_empty()
                    && (!events.first_output().await
                        || !events.emit("tool.generating", json!({"name": name})).await)
                {
                    return Err(ModelError::OutputClosed);
                }
            }
        }
    }
    text.full.push_str(&text.pending);
    reasoning.full.push_str(&reasoning.pending);
    let mut message = Message {
        content: (!text.full.is_empty()).then_some(text.full),
        refusal: (!refusal.is_empty()).then_some(refusal),
        reasoning_content: (!reasoning.full.is_empty()).then_some(reasoning.full),
        reasoning_details: (!reasoning_details.is_empty()).then_some(reasoning_details),
        tool_calls: calls.into_values().collect(),
        ..Message::default()
    };
    message.normalize_finish_reason(finish_reason);
    if !finished && message.finish_reason.as_deref() != Some(CONTENT_FILTER) {
        return Err(ModelError::IncompleteStream);
    }
    let mut visible = scrubber.push(&text.pending);
    visible.push_str(&scrubber.finish(true).0);
    if !visible.is_empty() {
        partial.push_str(&visible);
        if !events.first_output().await || !events.delta(&visible).await {
            return Err(ModelError::OutputClosed);
        }
    }
    if !reasoning.pending.is_empty()
        && (!events.first_output().await
            || !events
                .emit("reasoning.delta", json!({"text": reasoning.pending}))
                .await)
    {
        return Err(ModelError::OutputClosed);
    }
    Ok((message, usage))
}

fn append_tool_delta(call: &mut ToolCall, delta: ToolDelta) {
    if let Some(id) = delta.id {
        call.id.push_str(&id);
    }
    if let Some(kind) = delta.kind {
        call.kind = kind;
    }
    if let Some(function) = delta.function {
        if let Some(name) = function.name {
            call.function.name.push_str(&name);
        }
        if let Some(arguments) = function.arguments {
            call.function.arguments.push_str(&arguments);
        }
    }
}

#[derive(Default)]
struct StreamText {
    pending: String,
    full: String,
}

impl StreamText {
    fn is_empty(&self) -> bool {
        self.full.is_empty() && self.pending.is_empty()
    }

    fn push(&mut self, text: &str, secret: Option<&str>) -> String {
        let Some(secret) = secret else {
            self.full.push_str(text);
            return text.to_owned();
        };
        self.pending.push_str(text);
        self.pending = self.pending.replace(secret, "[REDACTED]");
        // A provider can split an echoed key across SSE chunks. Hold only the ambiguous suffix.
        let suffix = secret
            .char_indices()
            .map(|(end, _)| end)
            .rev()
            .find(|&end| self.pending.ends_with(&secret[..end]))
            .unwrap_or_default();
        let pending = self.pending.split_off(self.pending.len() - suffix);
        let visible = std::mem::replace(&mut self.pending, pending);
        self.full.push_str(&visible);
        visible
    }
}
