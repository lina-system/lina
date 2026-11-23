use std::collections::BTreeMap;

use eventsource_stream::Eventsource as _;
use futures_util::StreamExt as _;
use reqwest::Response;
use serde_json::json;

use super::{Completion, Message, ModelError, ToolCall, ToolDelta, Usage};
use crate::protocol::Events;

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
    let mut reasoning = StreamText::default();

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
                let visible = text.push(&content, secret);
                if !visible.is_empty() {
                    partial.push_str(&visible);
                    if !events.first_output().await || !events.delta(&visible).await {
                        return Err(ModelError::OutputClosed);
                    }
                }
            }
            if let Some(content) = delta.reasoning_content {
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
    if !text.pending.is_empty() {
        partial.push_str(&text.pending);
        if !events.first_output().await || !events.delta(&text.pending).await {
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
    reasoning.full.push_str(&reasoning.pending);
    let message = Message {
        content: (!partial.is_empty()).then(|| partial.clone()),
        reasoning_content: (!reasoning.full.is_empty()).then_some(reasoning.full),
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
