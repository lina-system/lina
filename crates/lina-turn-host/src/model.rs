use std::collections::VecDeque;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use reqwest::{Client, Response, Url};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::protocol::TurnPlan;

#[derive(Debug, thiserror::Error)]
pub(crate) enum ModelError {
    #[error("invalid model endpoint")]
    InvalidEndpoint,
    #[error("profile-scoped model credentials are unavailable")]
    MissingCredentials,
    #[error("invalid model request timeout")]
    InvalidTimeout,
    #[error("invalid session affinity headers")]
    InvalidSessionAffinity,
    #[error("model request failed")]
    Transport,
    #[error("model endpoint returned HTTP {status}")]
    Http { status: u16, summary: String },
    #[error("invalid model response")]
    InvalidResponse,
    #[error("model stream ended before completion")]
    IncompleteStream,
    #[error("host output closed")]
    OutputClosed,
}

pub(crate) struct ModelClient {
    http: Client,
    key: Option<HeaderValue>,
    last_stream_probe: Mutex<Option<Instant>>,
}

#[derive(Clone, Copy)]
pub(crate) enum RequestMode {
    Stream,
    Json,
    Probe,
}

impl ModelClient {
    pub(crate) fn new(secret: Option<&str>) -> Result<Self, ModelError> {
        let key = secret
            .map(|value| {
                let mut header = HeaderValue::from_str(&format!("Bearer {value}"))
                    .map_err(|_| ModelError::MissingCredentials)?;
                header.set_sensitive(true);
                Ok::<_, ModelError>(header)
            })
            .transpose()?;
        let http = Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .retry(reqwest::retry::never())
            .no_proxy()
            .connect_timeout(Duration::from_secs(10))
            .read_timeout(Duration::from_secs(120))
            .pool_idle_timeout(Duration::from_secs(90))
            .build()
            .map_err(|_| ModelError::Transport)?;
        Ok(Self {
            http,
            key,
            last_stream_probe: Mutex::new(None),
        })
    }

    pub(crate) fn claim_stream_probe(&self) -> bool {
        let Ok(mut last) = self.last_stream_probe.lock() else {
            return false;
        };
        let now = Instant::now();
        if last.is_some_and(|previous| now.duration_since(previous) < Duration::from_secs(60)) {
            return false;
        }
        *last = Some(now);
        true
    }

    pub(crate) fn endpoint(&self, base_url: &str) -> Result<Url, ModelError> {
        let mut url = Url::parse(base_url).map_err(|_| ModelError::InvalidEndpoint)?;
        if !url.username().is_empty()
            || url.password().is_some()
            || url.query().is_some()
            || url.fragment().is_some()
        {
            return Err(ModelError::InvalidEndpoint);
        }
        let loopback = match url.host() {
            Some(url::Host::Ipv4(addr)) => addr.is_loopback(),
            Some(url::Host::Ipv6(addr)) => addr.is_loopback(),
            _ => false,
        };
        match url.scheme() {
            "http" if loopback => {}
            "https" if loopback || self.key.is_some() => {}
            "https" => return Err(ModelError::MissingCredentials),
            _ => return Err(ModelError::InvalidEndpoint),
        }
        let path = url.path().trim_end_matches('/');
        let path = if path.ends_with("/chat/completions") {
            path.to_owned()
        } else {
            format!("{path}/chat/completions")
        };
        url.set_path(&path);
        Ok(url)
    }

    pub(crate) async fn request(
        &self,
        endpoint: &Url,
        plan: &TurnPlan,
        messages: &[Map<String, Value>],
        mode: RequestMode,
        secret: Option<&str>,
    ) -> Result<Response, ModelError> {
        let body = request_body(plan, messages, mode);
        self.send(
            endpoint,
            &body,
            plan.params.get("timeout"),
            plan.params.get("session_affinity"),
            secret,
        )
        .await
    }

    pub(crate) async fn request_summary(
        &self,
        endpoint: &Url,
        plan: &TurnPlan,
        mut body: Map<String, Value>,
        secret: Option<&str>,
    ) -> Result<Option<String>, ModelError> {
        let timeout = body.shift_remove("timeout");
        for _ in 0..2 {
            let response = self
                .send(
                    endpoint,
                    &body,
                    timeout.as_ref(),
                    plan.params.get("session_affinity"),
                    secret,
                )
                .await?;
            if let Some(text) = summary_text(response).await? {
                return Ok(Some(text));
            }
        }
        Ok(None)
    }

    async fn send(
        &self,
        endpoint: &Url,
        body: &Map<String, Value>,
        timeout: Option<&Value>,
        affinity: Option<&Value>,
        secret: Option<&str>,
    ) -> Result<Response, ModelError> {
        let mut request = self.http.post(endpoint.clone()).json(body);
        if let Some(timeout) = timeout {
            let seconds = timeout.as_f64().ok_or(ModelError::InvalidTimeout)?;
            let timeout = Duration::try_from_secs_f64(seconds)
                .ok()
                .filter(|timeout| !timeout.is_zero())
                .ok_or(ModelError::InvalidTimeout)?;
            request = request.timeout(timeout);
        }
        if let Some(affinity) = affinity {
            request = request.headers(affinity_headers(affinity)?);
        }
        if let Some(key) = &self.key {
            request = request.header(reqwest::header::AUTHORIZATION, key.clone());
        }
        let mut response = request.send().await.map_err(|_| ModelError::Transport)?;
        if !response.status().is_success() {
            let status = response.status().as_u16();
            let summary = if (400..600).contains(&status) {
                tokio::time::timeout(Duration::from_secs(1), error_summary(&mut response, secret))
                    .await
                    .ok()
                    .flatten()
            } else {
                None
            };
            let summary = summary.unwrap_or_else(|| format!("HTTP {status}"));
            return Err(ModelError::Http { status, summary });
        }
        Ok(response)
    }
}

async fn summary_text(response: Response) -> Result<Option<String>, ModelError> {
    let completion: Value = response
        .json()
        .await
        .map_err(|_| ModelError::InvalidResponse)?;
    if !completion["error"].is_null() {
        return Err(ModelError::InvalidResponse);
    }
    let choices = completion["choices"]
        .as_array()
        .filter(|choices| !choices.is_empty())
        .ok_or(ModelError::InvalidResponse)?;
    let message = &choices[0]["message"];
    let has_calls = message["tool_calls"]
        .as_array()
        .is_some_and(|calls| !calls.is_empty());
    let text = match &message["content"] {
        Value::Null => "",
        Value::String(text) => text.trim(),
        _ => return Err(ModelError::InvalidResponse),
    };
    let generated_tokens = ["completion_tokens", "output_tokens"].iter().any(|field| {
        completion["usage"][*field]
            .as_f64()
            .is_some_and(|value| value > 0.0)
    });
    if text == "Connect timeout, please try again later."
        && choices.len() == 1
        && !has_calls
        && !generated_tokens
    {
        return Ok(None);
    }
    let text = if text.is_empty() && !has_calls {
        message["refusal"].as_str().unwrap_or_default().trim()
    } else {
        text
    };
    Ok((!text.is_empty()).then(|| text.to_owned()))
}

async fn error_summary(response: &mut Response, secret: Option<&str>) -> Option<String> {
    const MAX_BODY_BYTES: usize = 16 * 1024;
    let mut body = Vec::new();
    while let Some(chunk) = response.chunk().await.ok()? {
        if chunk.len() > MAX_BODY_BYTES - body.len() {
            return None;
        }
        body.extend_from_slice(&chunk);
    }
    let data: Value = serde_json::from_slice(&body).ok()?;
    let message = data.pointer("/error/message")?.as_str()?;
    let clean: String = message.chars().filter(|ch| !ch.is_control()).collect();
    let clean = match secret {
        Some(secret) => clean.replace(secret, "[REDACTED]"),
        None => clean,
    };
    let summary: String = clean.trim().chars().take(512).collect();
    (!summary.is_empty()).then_some(summary)
}

fn affinity_headers(value: &Value) -> Result<HeaderMap, ModelError> {
    const ALLOWED: &[&str] = &[
        "session-id",
        "x-opencode-session",
        "x-litellm-session-id",
        "x-grok-conv-id",
    ];
    let entries = value
        .as_object()
        .ok_or(ModelError::InvalidSessionAffinity)?;
    let mut headers = HeaderMap::new();
    for (name, value) in entries {
        let allowed = ALLOWED
            .iter()
            .find(|allowed| name == **allowed)
            .ok_or(ModelError::InvalidSessionAffinity)?;
        let id = value
            .as_str()
            .filter(|id| {
                (1..=128).contains(&id.len())
                    && id
                        .bytes()
                        .all(|c| c.is_ascii_alphanumeric() || b"-_.:".contains(&c))
            })
            .ok_or(ModelError::InvalidSessionAffinity)?;
        let mut header =
            HeaderValue::from_str(id).map_err(|_| ModelError::InvalidSessionAffinity)?;
        header.set_sensitive(true);
        headers.insert(HeaderName::from_static(allowed), header);
    }
    Ok(headers)
}

fn request_body(
    plan: &TurnPlan,
    history: &[Map<String, Value>],
    mode: RequestMode,
) -> Map<String, Value> {
    // Agent history carries local fields such as timestamps, row IDs, and display metadata.
    const MESSAGE_FIELDS: &[&str] = &[
        "role",
        "content",
        "name",
        "cache_control",
        "function_call",
        "tool_calls",
        "tool_call_id",
        "reasoning_content",
        "reasoning_details",
        "refusal",
    ];
    // These parameters are request-body fields. Transport overrides and extra headers never
    // cross this boundary, and a plan cannot replace the resolved model or transcript.
    const PARAMS: &[&str] = &[
        "frequency_penalty",
        "logit_bias",
        "logprobs",
        "max_completion_tokens",
        "max_tokens",
        "parallel_tool_calls",
        "presence_penalty",
        "prompt_cache_key",
        "prompt_cache_retention",
        "reasoning_effort",
        "response_format",
        "seed",
        "service_tier",
        "stop",
        "stream_options",
        "temperature",
        "tool_choice",
        "top_logprobs",
        "top_p",
    ];
    let mut messages = vec![Map::from_iter([
        ("role".into(), json!("system")),
        ("content".into(), json!(plan.system)),
    ])];
    messages.extend(history.iter().map(|item| {
        let mut wire: Map<_, _> = item
            .iter()
            .filter(|(key, _)| MESSAGE_FIELDS.contains(&key.as_str()))
            .map(|(key, value)| (key.clone(), value.clone()))
            .collect();
        if item.get("role").and_then(Value::as_str) == Some("tool") {
            wire.shift_remove("name");
        }
        if let Some(calls) = wire.get_mut("tool_calls").and_then(Value::as_array_mut) {
            for call in calls {
                if let Some(name) = call.pointer_mut("/function/name")
                    && let Some(raw) = name.as_str()
                    && (raw.is_empty()
                        || raw.len() > 64
                        || raw.chars().any(|ch| !valid_tool_name_char(ch)))
                {
                    // Keep rejected names verbatim in the transcript; strict providers validate replay.
                    let coerced: String = raw
                        .trim()
                        .split(|ch| ch == '_' || !valid_tool_name_char(ch))
                        .filter(|part| !part.is_empty())
                        .collect::<Vec<_>>()
                        .join("_")
                        .chars()
                        .take(64)
                        .collect();
                    *name = json!(if coerced.is_empty() {
                        "invalid_tool_call"
                    } else {
                        &coerced
                    });
                }
                if let Some(args) = call.pointer_mut("/function/arguments")
                    && let Some(raw) = args.as_str()
                    && let Ok(mut parsed) = serde_json::from_str::<Value>(raw)
                {
                    parsed.sort_all_objects();
                    *args = Value::String(ascii_json(&parsed));
                }
            }
        }
        wire
    }));
    let mut body: Map<_, _> = plan
        .params
        .iter()
        .filter(|(key, _)| PARAMS.contains(&key.as_str()))
        .map(|(key, value)| (key.clone(), value.clone()))
        .collect();
    body.insert("model".into(), json!(plan.model));
    body.insert("messages".into(), json!(messages));
    match mode {
        RequestMode::Stream => {
            body.insert("stream".into(), json!(true));
        }
        RequestMode::Json => {
            body.insert("stream".into(), json!(false));
        }
        RequestMode::Probe => {
            body.remove("stream_options");
        }
    }
    if !plan.tools.is_empty() {
        body.insert("tools".into(), json!(plan.tools));
    }
    body
}

fn valid_tool_name_char(ch: char) -> bool {
    ch.is_ascii_alphanumeric() || matches!(ch, '_' | '-')
}

fn ascii_json(value: &Value) -> String {
    let json = value.to_string();
    if json.bytes().all(|byte| byte < 0x7f) {
        return json;
    }
    let mut ascii = String::with_capacity(json.len());
    for ch in json.chars() {
        if ch.is_ascii() && ch != '\u{7f}' {
            ascii.push(ch);
        } else {
            for unit in ch.encode_utf16(&mut [0; 2]) {
                ascii.push_str(&format!("\\u{unit:04x}"));
            }
        }
    }
    ascii
}

#[derive(Default, Deserialize, serde::Serialize)]
pub(crate) struct Usage {
    #[serde(default)]
    prompt_tokens: u64,
    #[serde(default)]
    input_tokens: u64,
    #[serde(default)]
    initial_prompt_tokens: u64,
    #[serde(default)]
    completion_tokens: u64,
    #[serde(default)]
    output_tokens: u64,
    prompt_tokens_details: Option<PromptDetails>,
    #[serde(default)]
    cache_read_input_tokens: u64,
    #[serde(default)]
    prompt_cache_hit_tokens: u64,
    #[serde(default)]
    cached_tokens: u64,
    #[serde(default)]
    cache_creation_input_tokens: u64,
    #[serde(default)]
    cache_write_tokens: u64,
    output_tokens_details: Option<CompletionDetails>,
    completion_tokens_details: Option<CompletionDetails>,
}

#[derive(Default, Deserialize, serde::Serialize)]
struct PromptDetails {
    #[serde(default)]
    cached_tokens: u64,
    #[serde(default)]
    cache_write_tokens: u64,
    #[serde(default)]
    cache_creation_input_tokens: u64,
}

#[derive(Deserialize, serde::Serialize)]
struct CompletionDetails {
    #[serde(default)]
    reasoning_tokens: u64,
}

#[derive(Default)]
pub(crate) struct TurnUsage {
    prompt: u64,
    input: u64,
    latest_prompt: Option<u64>,
    cache_read: u64,
    cache_write: u64,
    completion: u64,
    reasoning: u64,
    pub(crate) calls: usize,
    timings: VecDeque<(f64, f64)>,
}

impl TurnUsage {
    pub(crate) fn add(&mut self, usage: &Usage, duration: Duration) {
        let prompt = first_nonzero([usage.prompt_tokens, usage.input_tokens]);
        let output = first_nonzero([usage.completion_tokens, usage.output_tokens]);
        let default_details = PromptDetails::default();
        let details = usage
            .prompt_tokens_details
            .as_ref()
            .unwrap_or(&default_details);
        let cache_read = first_nonzero([
            details.cached_tokens,
            usage.cache_read_input_tokens,
            usage.prompt_cache_hit_tokens,
            usage.cached_tokens,
        ]);
        let cache_write = first_nonzero([
            details.cache_write_tokens,
            details.cache_creation_input_tokens,
            usage.cache_creation_input_tokens,
            usage.cache_write_tokens,
        ]);
        let input = prompt
            .saturating_sub(cache_read)
            .saturating_sub(cache_write);
        self.input = self.input.saturating_add(input);
        self.cache_read = self.cache_read.saturating_add(cache_read);
        self.cache_write = self.cache_write.saturating_add(cache_write);
        self.prompt = self
            .prompt
            .saturating_add(input.saturating_add(cache_read).saturating_add(cache_write));
        self.completion = self.completion.saturating_add(output);
        let last = first_nonzero([usage.initial_prompt_tokens, prompt]);
        if last > 0 {
            self.latest_prompt = Some(last);
        }
        let reasoning = first_nonzero([
            usage
                .output_tokens_details
                .as_ref()
                .map_or(0, |details| details.reasoning_tokens),
            usage
                .completion_tokens_details
                .as_ref()
                .map_or(0, |details| details.reasoning_tokens),
        ]);
        self.reasoning = self.reasoning.saturating_add(reasoning);
        if self.timings.len() == 10 {
            self.timings.pop_front();
        }
        self.timings.push_back((
            duration.as_secs_f64(),
            f64::from(u32::try_from(output).unwrap_or(u32::MAX)),
        ));
    }

    pub(crate) fn payload(&self, model: &str) -> Value {
        let mut usage = json!({"model": model,
               "input": self.input, "output": self.completion,
               "reasoning": self.reasoning, "prompt": self.prompt, "completion": self.completion,
               "total": self.prompt.saturating_add(self.completion), "calls": self.calls,
               "cache_read": self.cache_read, "cache_write": self.cache_write});
        if let Some(latest_prompt) = self.latest_prompt {
            usage["context_used"] = json!(latest_prompt);
        }
        let (latency, tokens) = self
            .timings
            .iter()
            .copied()
            .fold((0.0, 0.0), |(latency, tokens), (seconds, output)| {
                (latency + seconds, tokens + output)
            });
        let count = f64::from(u32::try_from(self.timings.len()).unwrap_or_default());
        for (name, value) in [
            ("avg_latency_s", latency / count),
            ("avg_tps", tokens / latency),
        ] {
            if value > 0.0 && value < 1e6 {
                usage[name] = json!((value * 10.0).round_ties_even() / 10.0);
            }
        }
        usage
    }
}

fn first_nonzero<const N: usize>(values: [u64; N]) -> u64 {
    values
        .into_iter()
        .find(|&value| value != 0)
        .unwrap_or_default()
}

#[derive(Default, Deserialize)]
struct Completion {
    #[serde(default)]
    choices: Vec<Choice>,
    usage: Option<Usage>,
    error: Option<Value>,
}

#[derive(Deserialize)]
struct Choice {
    message: Option<Message>,
    delta: Option<Delta>,
    finish_reason: Option<String>,
}

#[derive(Default, Deserialize)]
pub(crate) struct Message {
    pub(crate) content: Option<String>,
    pub(crate) reasoning_content: Option<String>,
    #[serde(default)]
    pub(crate) tool_calls: Vec<ToolCall>,
    #[serde(skip)]
    pub(crate) finish_reason: Option<String>,
}

#[derive(Default, Deserialize, Serialize)]
pub(crate) struct ToolCall {
    pub(crate) id: String,
    #[serde(rename = "type")]
    kind: String,
    pub(crate) function: Function,
}

impl ToolCall {
    pub(crate) fn valid_function(&self) -> bool {
        self.kind == "function" && !self.id.is_empty()
    }
}

#[derive(Default, Deserialize, Serialize)]
pub(crate) struct Function {
    pub(crate) name: String,
    pub(crate) arguments: String,
}

#[derive(Deserialize)]
struct Delta {
    content: Option<String>,
    #[serde(alias = "reasoning")]
    reasoning_content: Option<String>,
    tool_calls: Option<Vec<ToolDelta>>,
}

#[derive(Deserialize)]
struct ToolDelta {
    index: usize,
    id: Option<String>,
    #[serde(rename = "type")]
    kind: Option<String>,
    function: Option<FunctionDelta>,
}

#[derive(Deserialize)]
struct FunctionDelta {
    name: Option<String>,
    arguments: Option<String>,
}

pub(crate) async fn nonstream(response: Response) -> Result<(Message, Option<Usage>), ModelError> {
    let reply: Completion = response
        .json()
        .await
        .map_err(|_| ModelError::InvalidResponse)?;
    if reply.error.is_some() {
        return Err(ModelError::InvalidResponse);
    }
    let choice = reply
        .choices
        .into_iter()
        .next()
        .ok_or(ModelError::InvalidResponse)?;
    let mut message = choice.message.ok_or(ModelError::InvalidResponse)?;
    message.finish_reason = choice.finish_reason;
    Ok((message, reply.usage))
}

mod streaming;
pub(crate) use streaming::stream;
