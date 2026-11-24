use std::collections::BTreeMap;

use eventsource_stream::Eventsource as _;
use futures_util::StreamExt as _;
use reqwest::Response;
use serde_json::json;

use super::{Completion, Message, ModelError, ToolCall, ToolDelta, Usage};
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

pub(crate) async fn stream(
    response: Response,
    events: &mut Events<'_>,
    partial: &mut String,
    secret: Option<&str>,
) -> Result<(Message, Option<Usage>), ModelError> {
    let mut stream = response.bytes_stream().eventsource();
    let mut calls = BTreeMap::new();
    let mut usage = None;
    let mut finished = false;
    let mut finish_reason = None;
    let mut text = StreamText::default();
    let mut scrubber = VisibleText::default();
    let mut reasoning = StreamText::default();
    let mut reasoning_details = Vec::new();

    while let Some(event) = stream.next().await {
        let event = event.map_err(|_| ModelError::Transport)?;
        if event.data.trim() == "[DONE]" {
            finished = true;
            break;
        }
        let chunk: Completion =
            serde_json::from_str(&event.data).map_err(|_| ModelError::InvalidResponse)?;
        if chunk.error.is_some() {
            return Err(ModelError::InvalidResponse);
        }
        if let Some(sample) = chunk.usage {
            usage = Some(sample);
        }
        let Some(choice) = chunk.choices.into_iter().next() else {
            continue;
        };
        if let Some(reason) = choice.finish_reason {
            finished = true;
            finish_reason = Some(reason);
        }
        if let Some(delta) = choice.delta {
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
    if !finished {
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
    text.full.push_str(&text.pending);
    if !reasoning.pending.is_empty()
        && (!events.first_output().await
            || !events
                .emit("reasoning.delta", json!({"text": reasoning.pending}))
                .await)
    {
        return Err(ModelError::OutputClosed);
    }
    reasoning.full.push_str(&reasoning.pending);
    let message = Message {
        content: (!text.full.is_empty()).then_some(text.full),
        reasoning_content: (!reasoning.full.is_empty()).then_some(reasoning.full),
        reasoning: None,
        reasoning_details: (!reasoning_details.is_empty()).then_some(reasoning_details),
        tool_calls: calls.into_values().collect(),
        finish_reason,
    };
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
