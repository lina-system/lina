use std::collections::{BTreeMap, VecDeque};
use std::error::Error;
use std::io::ErrorKind;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use reqwest::{Client, Response, Url};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::protocol::TurnPlan;

pub(crate) const CONTENT_FILTER: &str = "content_filter";
const MAX_ERROR_BYTES: usize = 16 * 1024;

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
    #[error("model connection interrupted")]
    TransientTransport,
    #[error("model endpoint returned HTTP {status}")]
    Http {
        status: u16,
        detail: Box<HttpDetail>,
    },
    #[error("invalid model response")]
    InvalidResponse,
    #[error("model stream ended before completion")]
    IncompleteStream,
    #[error("host output closed")]
    OutputClosed,
}

#[derive(Debug, Serialize)]
pub(crate) struct HttpDetail {
    message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    code: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    retry_after: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    body_retry_after: Option<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    body_reset: BTreeMap<&'static str, Value>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    error_reset: BTreeMap<&'static str, Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    rate_limit_reset: Option<String>,
}

impl ModelError {
    pub(crate) fn failure(&self, attempts: usize) -> Option<Value> {
        match self {
            Self::Http { status, detail } if (400..600).contains(status) => {
                let mut failure = json!(detail);
                failure["status_code"] = json!(status);
                failure["attempts"] = json!(attempts);
                Some(failure)
            }
            Self::TransientTransport => {
                Some(json!({"transport": "connection", "attempts": attempts}))
            }
            _ => None,
        }
    }

    fn from_transport(error: reqwest::Error) -> Self {
        if error.is_timeout() {
            return Self::TransientTransport;
        }
        // A reqwest connect error can also be a permanent TLS verification failure.
        let mut cause = error.source();
        while let Some(err) = cause {
            if err.downcast_ref::<std::io::Error>().is_some_and(|io| {
                matches!(
                    io.kind(),
                    ErrorKind::ConnectionRefused
                        | ErrorKind::ConnectionReset
                        | ErrorKind::ConnectionAborted
                        | ErrorKind::BrokenPipe
                        | ErrorKind::TimedOut
                        | ErrorKind::UnexpectedEof
                        | ErrorKind::NetworkUnreachable
                        | ErrorKind::HostUnreachable
                )
            }) || err
                .downcast_ref::<hyper::Error>()
                .is_some_and(|http| http.is_incomplete_message() || http.is_closed())
            {
                return Self::TransientTransport;
            }
            cause = err.source();
        }
        Self::Transport
    }
}

pub(crate) struct ModelClient {
    http: Client,
    stream_http: Mutex<(Duration, Client)>,
    proxy: Option<reqwest::Proxy>,
    key: Option<HeaderValue>,
    authenticated_proxy: bool,
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
        let mut authenticated_proxy = false;
        let proxy = if let Some(raw) = std::env::var_os("LINA_TURN_MODEL_PROXY") {
            let mut url = Url::parse(raw.to_str().ok_or(ModelError::Transport)?)
                .map_err(|_| ModelError::Transport)?;
            authenticated_proxy = !url.username().is_empty() || url.password().is_some();
            // httpx sends the destination name to the SOCKS5 server for resolution.
            if url.scheme() == "socks5" {
                url.set_scheme("socks5h")
                    .map_err(|_| ModelError::Transport)?;
            }
            Some(reqwest::Proxy::all(url).map_err(|_| ModelError::Transport)?)
        } else {
            None
        };
        let default_read = Duration::from_secs(120);
        let http = Self::http_client(proxy.as_ref(), default_read)?;
        Ok(Self {
            stream_http: Mutex::new((default_read, http.clone())),
            proxy,
            http,
            key,
            authenticated_proxy,
            last_stream_probe: Mutex::new(None),
        })
    }

    fn http_client(
        proxy: Option<&reqwest::Proxy>,
        read_timeout: Duration,
    ) -> Result<Client, ModelError> {
        let mut builder = Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .retry(reqwest::retry::never())
            .no_proxy();
        if let Some(proxy) = proxy {
            builder = builder.proxy(proxy.clone());
        }
        builder
            .connect_timeout(Duration::from_secs(10))
            .read_timeout(read_timeout)
            .pool_idle_timeout(Duration::from_secs(90))
            .build()
            .map_err(|_| ModelError::Transport)
    }

    fn stream_client(&self, timeout: &Value) -> Result<Client, ModelError> {
        let seconds = timeout.as_f64().ok_or(ModelError::InvalidTimeout)?;
        let read = Duration::try_from_secs_f64(seconds).map_err(|_| ModelError::InvalidTimeout)?;
        // reqwest sets read patience on the pool. Keep one configured pool across
        // rounds; outstanding requests retain their own client when policy changes.
        let mut cached = self.stream_http.lock().map_err(|_| ModelError::Transport)?;
        if cached.0 != read {
            *cached = (read, Self::http_client(self.proxy.as_ref(), read)?);
        }
        Ok(cached.1.clone())
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
        let http = match (mode, plan.params.get("stream_read_timeout")) {
            (RequestMode::Stream, Some(timeout)) => self.stream_client(timeout)?,
            _ => self.http.clone(),
        };
        self.send(
            endpoint,
            http.post(endpoint.clone()).json(&body),
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
                    self.http.post(endpoint.clone()).json(&body),
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
        mut request: reqwest::RequestBuilder,
        timeout: Option<&Value>,
        affinity: Option<&Value>,
        secret: Option<&str>,
    ) -> Result<Response, ModelError> {
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
        let mut response = request.send().await.map_err(ModelError::from_transport)?;
        if !response.status().is_success() {
            let status = response.status().as_u16();
            // For plaintext HTTP the proxy itself can supply any status, headers and body,
            // including an echo of Proxy-Authorization. Only the status is safe to forward.
            if self.authenticated_proxy && (endpoint.scheme() == "http" || status == 407) {
                return Err(provider_error(
                    status,
                    &Value::Null,
                    &HeaderMap::new(),
                    secret,
                ));
            }
            let body = if (400..600).contains(&status) {
                tokio::time::timeout(Duration::from_secs(1), error_body(&mut response))
                    .await
                    .ok()
                    .flatten()
            } else {
                None
            };
            return Err(provider_error(
                status,
                body.as_ref().unwrap_or(&Value::Null),
                response.headers(),
                secret,
            ));
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

async fn error_body(response: &mut Response) -> Option<Value> {
    let mut body = Vec::new();
    while let Some(chunk) = response.chunk().await.ok()? {
        if chunk.len() > MAX_ERROR_BYTES - body.len() {
            return None;
        }
        body.extend_from_slice(&chunk);
    }
    serde_json::from_slice(&body).ok()
}

fn bounded_scalar(value: &Value) -> Option<&Value> {
    match value {
        Value::Number(_) => Some(value),
        Value::String(s) if s.len() <= 128 && !s.chars().any(char::is_control) => Some(value),
        _ => None,
    }
}

fn reset_fields(payload: &Value) -> BTreeMap<&'static str, Value> {
    ["resets_in_seconds", "resets_at", "reset_at", "retry_after"]
        .into_iter()
        .filter_map(|name| Some((name, bounded_scalar(payload.get(name)?)?.clone())))
        .collect()
}

fn provider_error(
    status: u16,
    data: &Value,
    headers: &HeaderMap,
    secret: Option<&str>,
) -> ModelError {
    let error = data.get("error").filter(|v| v.is_object()).unwrap_or(data);
    let message = error["message"].as_str().unwrap_or_default();
    let clean: String = message.chars().filter(|ch| !ch.is_control()).collect();
    let clean = match secret {
        Some(secret) => clean.replace(secret, "[REDACTED]"),
        None => clean,
    };
    let short_field = |value: &Value| {
        bounded_scalar(value).map(|value| match value.as_str() {
            Some(s) => s.to_owned(),
            None => value.to_string(),
        })
    };
    let header = |name: &str| {
        headers
            .get(name)?
            .to_str()
            .ok()
            .filter(|s| s.len() <= 128 && !s.chars().any(char::is_control))
            .map(str::to_owned)
    };
    let detail = HttpDetail {
        message: if clean.trim().is_empty() {
            format!("HTTP {status}")
        } else {
            clean.trim().chars().take(512).collect()
        },
        code: short_field(&error["code"]).or_else(|| short_field(&error["type"])),
        body_retry_after: short_field(&error["retry_after"]),
        retry_after: header("retry-after"),
        body_reset: reset_fields(data),
        error_reset: reset_fields(&data["error"]),
        rate_limit_reset: header("x-ratelimit-reset"),
    };
    ModelError::Http {
        status,
        detail: Box::new(detail),
    }
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
    initial_cached_tokens: u64,
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
    speed: Option<String>,
    service_tier: Option<String>,
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
    service_tier: Option<String>,
    error: Option<serde::de::IgnoredAny>,
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
    refusal: Option<String>,
    pub(crate) reasoning_content: Option<String>,
    reasoning: Option<String>,
    pub(crate) reasoning_details: Option<Vec<Value>>,
    #[serde(default)]
    pub(crate) tool_calls: Vec<ToolCall>,
    #[serde(skip)]
    pub(crate) finish_reason: Option<String>,
}

impl Message {
    fn normalize_finish_reason(&mut self, finish_reason: Option<String>) {
        let refusal_only = self
            .refusal
            .as_deref()
            .is_some_and(|text| !text.trim().is_empty())
            && self
                .content
                .as_deref()
                .is_none_or(|text| text.trim().is_empty())
            && self.tool_calls.is_empty();
        self.finish_reason = match finish_reason.as_deref() {
            None | Some("stop") if refusal_only => Some(CONTENT_FILTER.to_owned()),
            _ => finish_reason,
        };
    }

    pub(crate) fn refusal_explanation(&self, secret: Option<&str>) -> Option<String> {
        [self.content.as_deref(), self.refusal.as_deref()]
            .into_iter()
            .flatten()
            .find_map(|text| {
                let (visible, _) = visible_text(text);
                let visible = match secret {
                    Some(secret) => visible.replace(secret, "[REDACTED]"),
                    None => visible,
                };
                let clean: String = visible
                    .chars()
                    .filter(|ch| !ch.is_control() || *ch == '\n')
                    .collect();
                let explanation = clean.trim();
                (!explanation.is_empty()).then(|| explanation.chars().take(500).collect())
            })
    }
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
    refusal: Option<String>,
    reasoning_content: Option<String>,
    reasoning: Option<String>,
    reasoning_details: Option<Vec<Value>>,
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
    let body = response.bytes().await.map_err(ModelError::from_transport)?;
    let reply: Completion =
        serde_json::from_slice(&body).map_err(|_| ModelError::InvalidResponse)?;
    if reply.error.is_some() {
        return Err(ModelError::InvalidResponse);
    }
    let choice = reply
        .choices
        .into_iter()
        .next()
        .ok_or(ModelError::InvalidResponse)?;
    let mut message = choice.message.ok_or(ModelError::InvalidResponse)?;
    message.normalize_finish_reason(choice.finish_reason);
    if message.reasoning_content.is_none() {
        message.reasoning_content = message.reasoning.take();
    }
    let usage = reply.usage.map(|mut usage| {
        if let Some(tier) = reply.service_tier.filter(|tier| !tier.trim().is_empty()) {
            usage.service_tier = Some(tier.trim().to_ascii_lowercase());
        }
        usage
    });
    Ok((message, usage))
}

mod streaming;
pub(crate) use streaming::{inline_reasoning, stream, visible_text};

#[cfg(test)]
mod tests {
    use super::{CONTENT_FILTER, Message};
    use serde_json::json;

    #[test]
    fn structured_refusal_promotes_only_without_a_live_payload() {
        let calls = json!([{
            "id": "call-1",
            "type": "function",
            "function": {"name": "read_file", "arguments": "{}"},
        }]);
        let cases = [
            (
                None,
                Some("I decline."),
                json!([]),
                Some("stop"),
                Some(CONTENT_FILTER),
            ),
            (
                Some(" \n"),
                Some("I decline."),
                json!([]),
                None,
                Some(CONTENT_FILTER),
            ),
            (None, Some(" \n"), json!([]), Some("stop"), Some("stop")),
            (
                Some("Visible text."),
                Some("I decline."),
                json!([]),
                None,
                None,
            ),
            (None, Some("I decline."), calls, Some("stop"), Some("stop")),
            (
                None,
                Some("I decline."),
                json!([]),
                Some("length"),
                Some("length"),
            ),
            (
                Some("Visible text."),
                None,
                json!([]),
                Some(CONTENT_FILTER),
                Some(CONTENT_FILTER),
            ),
        ];
        for (content, refusal, tool_calls, reason, expected) in cases {
            let mut message: Message = serde_json::from_value(json!({
                "content": content, "refusal": refusal, "tool_calls": tool_calls,
            }))
            .unwrap();
            message.normalize_finish_reason(reason.map(str::to_owned));
            assert_eq!(message.finish_reason.as_deref(), expected);
        }
    }

    #[test]
    fn refusal_explanation_excludes_private_reasoning_and_secrets() {
        let message: Message = serde_json::from_value(json!({
            "content": "<think>private inline reasoning</think>",
            "reasoning_content": "private structured reasoning",
            "refusal": "Provider declined token synthetic-key.\u{1b}",
        }))
        .unwrap();
        assert_eq!(
            message
                .refusal_explanation(Some("synthetic-key"))
                .as_deref(),
            Some("Provider declined token [REDACTED]."),
        );
    }
}
