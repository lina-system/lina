use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::OnceLock;
use std::sync::mpsc::{self, Receiver};
use std::time::Duration;

use serde_json::{Value, json};

const TIMEOUT: Duration = Duration::from_secs(10);
const KEY: &str = "synthetic-turn-host-key";
const AFFINITY: [&str; 4] = [
    "session-id",
    "x-opencode-session",
    "x-litellm-session-id",
    "x-grok-conv-id",
];

fn host_frame_schema() -> &'static jsonschema::Validator {
    static VALIDATOR: OnceLock<jsonschema::Validator> = OnceLock::new();
    VALIDATOR.get_or_init(|| {
        let schema: Value = serde_json::from_str(include_str!(
            "../../../docs/api/compute-host-frames.schema.json"
        ))
        .unwrap();
        jsonschema::validator_for(&schema).expect("generated compute-host frame schema")
    })
}

struct Host {
    child: Child,
    input: ChildStdin,
    lines: Receiver<Value>,
}

impl Host {
    fn spawn() -> Self {
        let mut child = Command::new(env!("CARGO_BIN_EXE_lina-turn-host"))
            .env_clear()
            .env("OPENAI_API_KEY", KEY)
            .env("HERMES_COMPUTE_HOST_HEARTBEAT_SECS", "0")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .expect("start turn host");
        let input = child.stdin.take().unwrap();
        let stdout = child.stdout.take().unwrap();
        let (sender, lines) = mpsc::channel();
        std::thread::spawn(move || {
            for line in BufReader::new(stdout).lines() {
                let line = line.unwrap();
                assert!(!line.contains(KEY), "model key appeared on the JSONL pipe");
                let frame: Value = serde_json::from_str(&line).unwrap();
                if let Err(error) = host_frame_schema().validate(&frame) {
                    panic!("host frame does not match the shared schema: {error}");
                }
                if sender.send(frame).is_err() {
                    break;
                }
            }
        });
        let host = Self {
            child,
            input,
            lines,
        };
        let hello = host.next();
        assert_eq!(hello["type"], "hello");
        assert_eq!(hello["host_pid"], host.child.id());
        assert!(!hello["build_sha"].as_str().unwrap().is_empty());
        host
    }

    fn next(&self) -> Value {
        loop {
            let frame = self.next_raw();
            let kind = frame["message"]["params"]["type"].as_str();
            if !matches!(kind, Some("thinking.delta" | "tool.generating"))
                && frame["message"]["method"] != "compute_host.activity"
            {
                return frame;
            }
        }
    }

    fn next_raw(&self) -> Value {
        self.lines.recv_timeout(TIMEOUT).expect("turn host frame")
    }

    fn send(&mut self, body: &Value) {
        writeln!(self.input, "{body}").unwrap();
        self.input.flush().unwrap();
    }

    fn begin(&mut self, request_id: &str, base_url: &str, stream: bool) {
        let affinity = AFFINITY.map(|name| (name, json!("session-17")));
        self.begin_with(
            request_id,
            base_url,
            stream,
            Value::Object(
                affinity
                    .into_iter()
                    .map(|(key, value)| (key.into(), value))
                    .collect(),
            ),
        );
    }

    fn begin_with(&mut self, request_id: &str, base_url: &str, stream: bool, affinity: Value) {
        self.send(&json!({
            "type": "turn.start", "sid": "session", "request_id": request_id,
            "turn_id": request_id, "text": "ping", "cwd": std::env::current_dir().unwrap(),
            "terminal_backend": "local", "history": [{"role": "user", "content": "before"}],
            "history_version": 20,
            "turn_plan": {"system": "system prompt", "tools": [], "model": "local",
                          "base_url": base_url, "params": {"prompt_cache_key": "cache", "timeout": 5,
                                                           "session_affinity": affinity},
                          "stream": stream}
        }));
        assert_eq!(self.next()["type"], "turn.started");
        assert_eq!(event(&self.next())["type"], "message.start");
    }

    fn close(&mut self) {
        self.send(&json!({"type": "shutdown", "request_id": "close"}));
        let ack = self.next();
        assert_eq!(ack["type"], "shutdown.ack");
        assert_eq!(ack["request_id"], "close");
        assert!(
            self.lines.recv_timeout(TIMEOUT).is_err(),
            "host did not close stdout"
        );
        assert!(self.child.wait().unwrap().success());
    }
}

impl Drop for Host {
    fn drop(&mut self) {
        if self.child.try_wait().is_ok_and(|status| status.is_none()) {
            self.child.kill().unwrap();
            self.child.wait().unwrap();
        }
    }
}

fn event(frame: &Value) -> &Value {
    assert_eq!(frame["type"], "rpc");
    &frame["message"]["params"]
}

fn model() -> (TcpListener, String) {
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    let endpoint = format!("http://{}/v1", listener.local_addr().unwrap());
    (listener, endpoint)
}

fn request(listener: &TcpListener) -> (TcpStream, Value) {
    let (socket, _) = listener.accept().unwrap();
    socket.set_read_timeout(Some(TIMEOUT)).unwrap();
    let mut reader = BufReader::new(socket);
    let mut line = String::new();
    reader.read_line(&mut line).unwrap();
    assert_eq!(line, "POST /v1/chat/completions HTTP/1.1\r\n");
    let mut length = None;
    let mut authorization = None;
    let mut affinity = std::collections::BTreeSet::new();
    loop {
        line.clear();
        reader.read_line(&mut line).unwrap();
        if line == "\r\n" {
            break;
        }
        let (header, value) = line.split_once(':').unwrap();
        if header.eq_ignore_ascii_case("content-length") {
            length = Some(value.trim().parse().unwrap());
        }
        if header.eq_ignore_ascii_case("authorization") {
            authorization = Some(value.trim().to_owned());
        }
        if let Some(name) = AFFINITY
            .into_iter()
            .find(|name| header.eq_ignore_ascii_case(name))
        {
            assert_eq!(value.trim(), "session-17");
            assert!(affinity.insert(name));
        }
    }
    assert!(authorization.is_some_and(|header| header == format!("Bearer {KEY}")));
    assert_eq!(affinity, AFFINITY.into_iter().collect());
    let mut body = vec![0; length.unwrap()];
    reader.read_exact(&mut body).unwrap();
    (reader.into_inner(), serde_json::from_slice(&body).unwrap())
}

fn reply(mut socket: TcpStream, content_type: &str, body: &str) {
    write!(
        socket,
        "HTTP/1.1 200 OK\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
    .unwrap();
}

#[test]
fn same_process_completes_nonstream_and_stream_with_cached_prompt_fields() {
    let (model, base_url) = model();
    let server = std::thread::spawn(move || {
        let (socket, body) = request(&model);
        assert_eq!(body["prompt_cache_key"], "cache");
        assert!(body.get("timeout").is_none());
        assert!(body.get("session_affinity").is_none());
        assert_eq!(
            body["messages"][0],
            json!({"role": "system", "content": "system prompt"})
        );
        assert_eq!(body["stream"], false);
        reply(
            socket,
            "application/json",
            r#"{"choices":[{"message":{"content":"first"}}]}"#,
        );

        let (socket, body) = request(&model);
        assert_eq!(body["stream"], true);
        reply(
            socket,
            "text/event-stream",
            concat!(
                "data: {\"choices\":[{\"delta\":{\"content\":\"😀synthetic-turn\"}}]}\r\n\r\n",
                "data: {\"choices\":[{\"delta\":{\"content\":\"-host-key done\"},\"finish_reason\":\"stop\"}]}\r\n\r\n",
                "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\r\n\r\n",
                "data: [DONE]\r\n\r\n",
            ),
        );
    });
    let mut host = Host::spawn();
    host.begin("first", &base_url, false);
    assert_eq!(event(&host.next())["payload"]["text"], "first");
    let end = host.next();
    assert_eq!(end["type"], "turn.end");
    assert_eq!(end["message_count"], 3);
    assert_eq!(end["history_version"], 21);
    assert_eq!(end["messages"][0]["content"], "before");
    assert_eq!(
        end["new_messages"],
        json!([
            {"role": "user", "content": "ping"}, {"role": "assistant", "content": "first"}
        ])
    );

    host.begin("second", &base_url, true);
    let announce = host.next_raw();
    assert_eq!(event(&announce)["type"], "thinking.delta");
    assert!(
        !event(&announce)["payload"]["text"]
            .as_str()
            .unwrap()
            .is_empty()
    );
    let first_output = host.next_raw();
    assert_eq!(event(&first_output)["type"], "thinking.delta");
    assert_eq!(event(&first_output)["payload"]["text"], "");
    let delta = host.next();
    assert_eq!(event(&delta)["payload"]["text"], "😀");
    assert_eq!(event(&delta)["payload"]["offset"], 0);
    let delta = host.next();
    assert_eq!(event(&delta)["payload"]["text"], "[REDACTED] done");
    assert_eq!(event(&delta)["payload"]["offset"], 2);
    let settled = host.next_raw();
    assert_eq!(event(&settled)["type"], "thinking.delta");
    assert_eq!(event(&settled)["payload"]["text"], "");
    let completed = host.next();
    assert_eq!(event(&completed)["payload"]["usage"]["total"], 10);
    assert!(event(&completed)["payload"]["usage"]["avg_latency_s"].is_number());
    assert!(event(&completed)["payload"]["usage"]["avg_tps"].is_number());
    assert_eq!(event(&completed)["payload"]["text"], "😀[REDACTED] done");
    assert_eq!(host.next()["request_id"], "second");
    host.close();
    server.join().unwrap();
}

#[test]
fn unsupported_tool_settles_once_and_interrupt_cancels_an_inflight_http_request() {
    let (model, base_url) = model();
    let (sent, entered) = mpsc::channel();
    let (cancelled, released) = mpsc::channel();
    let server = std::thread::spawn(move || {
        let (socket, _) = request(&model);
        reply(
            socket,
            "application/json",
            r#"{"choices":[{"message":{"tool_calls":[{"id":"call","type":"function","function":{"name":"other","arguments":"{}"}}]}}]}"#,
        );
        let (socket, body) = request(&model);
        assert_eq!(body["messages"].as_array().unwrap().len(), 3);
        sent.send(()).unwrap();
        released.recv_timeout(TIMEOUT).unwrap();
        drop(socket);
    });
    let mut host = Host::spawn();
    host.begin("unsupported", &base_url, false);
    let error = host.next();
    assert_eq!(error["type"], "turn.error");
    assert_eq!(error["reason"], "unsupported_tool");
    host.begin("interrupt", &base_url, false);
    entered.recv_timeout(TIMEOUT).unwrap();
    host.send(&json!({"type": "interrupt", "sid": "session", "request_id": "stop"}));
    let ack = host.next();
    assert_eq!(ack["type"], "interrupt.ack");
    assert_eq!(ack["request_id"], "stop");
    assert_eq!(ack["applied"], true);
    assert_eq!(event(&host.next())["payload"]["status"], "interrupted");
    assert_eq!(host.next()["interrupted"], true);
    host.close();
    cancelled.send(()).unwrap();
    server.join().unwrap();
}

#[test]
fn invalid_session_affinity_is_rejected_before_http_egress() {
    let (listener, base_url) = model();
    listener.set_nonblocking(true).unwrap();
    let mut host = Host::spawn();
    for (attempt, affinity) in [
        json!({"authorization": "override"}),
        json!({"session-id": "id\r\nAuthorization: override"}),
        json!({"session-id": "é"}),
        json!({"session-id": "x".repeat(129)}),
    ]
    .into_iter()
    .enumerate()
    {
        host.begin_with(&format!("reject-{attempt}"), &base_url, false, affinity);
        let completed = host.next();
        assert_eq!(event(&completed)["payload"]["status"], "error");
        assert_eq!(
            event(&completed)["payload"]["error"],
            "invalid session affinity headers"
        );
        assert_eq!(host.next()["type"], "turn.end");
        assert_eq!(
            listener.accept().unwrap_err().kind(),
            std::io::ErrorKind::WouldBlock
        );
    }
    host.close();
}

#[test]
fn streaming_500_probes_nonstream_once_without_another_logical_call() {
    let (listener, base_url) = model();
    let (released, release) = mpsc::channel();
    let server = std::thread::spawn(move || {
        for (index, stream) in [Some(&Value::Bool(true)), None, Some(&Value::Bool(true))]
            .into_iter()
            .enumerate()
        {
            let (mut socket, body) = request(&listener);
            assert_eq!(body.get("stream"), stream);
            if stream.is_none() {
                assert!(body.get("stream_options").is_none());
            }
            let message = if index == 1 {
                "ignored diagnostic probe failure".to_owned()
            } else {
                format!("\u{0000}failure: {KEY}\r\n{}", "🙂".repeat(600))
            };
            let error =
                json!({"error":{"message":message,"details":"unrelated body field"}}).to_string();
            write!(socket, "HTTP/1.1 500 Internal Server Error\r\nContent-Type: application/json\r\nX-Debug: unrelated header\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{error}", error.len()).unwrap();
        }
        release.recv_timeout(TIMEOUT).unwrap();
        listener.set_nonblocking(true).unwrap();
        assert_eq!(
            listener.accept().unwrap_err().kind(),
            std::io::ErrorKind::WouldBlock
        );
    });
    let mut host = Host::spawn();
    for rid in ["first", "within-probe-window"] {
        host.begin(rid, &base_url, true);
        let completion = host.next();
        let payload = &event(&completion)["payload"];
        assert_eq!(payload["status"], "error");
        assert_eq!(payload["usage"]["calls"], 0);
        assert!(payload.get("provider_error").is_none());
        let end = host.next();
        assert_eq!(end["type"], "turn.end");
        assert_eq!(end["provider_error"]["status_code"], 500);
        assert_eq!(end["provider_error"]["attempts"], 1);
        let summary = end["provider_error"]["message"].as_str().unwrap();
        assert_eq!(summary.chars().count(), 512);
        assert!(summary.starts_with("failure: [REDACTED]🙂"));
        assert!(!summary.chars().any(char::is_control));
    }
    host.close();
    released.send(()).unwrap();
    server.join().unwrap();
}

#[test]
fn rejected_redirect_has_no_provider_error_metadata_or_followup_request() {
    let (listener, base_url) = model();
    let served_url = base_url.clone();
    let (released, release) = mpsc::channel();
    let server = std::thread::spawn(move || {
        let (mut socket, _) = request(&listener);
        write!(socket, "HTTP/1.1 302 Found\r\nLocation: {served_url}/redirect\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").unwrap();
        release.recv_timeout(TIMEOUT).unwrap();
        listener.set_nonblocking(true).unwrap();
        assert_eq!(
            listener.accept().unwrap_err().kind(),
            std::io::ErrorKind::WouldBlock
        );
    });
    let mut host = Host::spawn();
    host.begin("redirect", &base_url, false);
    assert_eq!(event(&host.next())["payload"]["status"], "error");
    let end = host.next();
    assert_eq!(end["type"], "turn.end");
    assert!(end.get("provider_error").is_none());
    host.close();
    released.send(()).unwrap();
    server.join().unwrap();
}

#[test]
fn interrupt_during_read_settles_the_accepted_tool_batch_without_replaying() {
    let (listener, base_url) = model();
    let (released, release) = mpsc::channel();
    let server = std::thread::spawn(move || {
        let (socket, _) = request(&listener);
        reply(
            socket,
            "application/json",
            r#"{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"reading","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"fixture.txt\"}"}}]}}]}"#,
        );
        release.recv_timeout(TIMEOUT).unwrap();
        listener.set_nonblocking(true).unwrap();
        assert_eq!(
            listener.accept().unwrap_err().kind(),
            std::io::ErrorKind::WouldBlock
        );
    });
    let mut host = Host::spawn();
    host.begin("tools", &base_url, false);
    assert_eq!(event(&host.next())["type"], "tool.start");
    assert_eq!(host.next()["op"], "authorize");
    host.send(&json!({"type": "interrupt", "sid": "session", "request_id": "stop"}));
    assert_eq!(host.next()["applied"], true);
    let done = host.next();
    assert_eq!(event(&done)["type"], "tool.complete");
    assert_eq!(event(&done)["payload"]["tool_id"], "reading");
    assert_eq!(event(&host.next())["payload"]["status"], "interrupted");
    let turn_end = host.next();
    let rows = turn_end["new_messages"].as_array().unwrap();
    assert_eq!(rows.len(), 4);
    assert_eq!(rows[1]["content"], "");
    assert_eq!(rows[1]["tool_calls"][0]["id"], "reading");
    assert_eq!(rows[2]["tool_call_id"], "reading");
    assert_eq!(rows[2]["content"], event(&done)["payload"]["result"]);
    assert_eq!(rows[3], json!({"role":"assistant","content":""}));
    host.close();
    released.send(()).unwrap();
    server.join().unwrap();
}

#[cfg(target_os = "linux")]
#[test]
fn streamed_tool_calls_wait_for_matching_read_policy_in_sequence() {
    use std::os::unix::fs::MetadataExt;
    use std::path::Path;

    let manifest = Path::new(env!("CARGO_MANIFEST_DIR")).join("Cargo.toml");
    let metadata = manifest.metadata().unwrap();
    let identity = json!({
        "dev": metadata.dev(), "ino": metadata.ino(), "size": metadata.size(),
        "mtime_ns": metadata.mtime() * 1_000_000_000 + metadata.mtime_nsec(),
        "ctime_ns": metadata.ctime() * 1_000_000_000 + metadata.ctime_nsec(),
    });
    let (model, base_url) = model();
    let server = std::thread::spawn(move || {
        let (socket, _) = request(&model);
        let arguments =
            |offset: u64| format!(r#"{{"path": "Cargo.toml", "offset": {offset}, "limit": 1}}"#);
        let chunk = |index, id, offset| {
            json!({
                "index": index,
                "id": id,
                "type": "function",
            "function": {"name": "read_file", "arguments": arguments(offset)},
            })
        };
        let body = json!({"choices":[{"delta":{
            "tool_calls":[chunk(1, "second", 2), chunk(0, "first", 1)]
        }}]});
        reply(
            socket,
            "text/event-stream",
            &format!(
                "data: {body}\n\ndata: {{\"usage\":{{\"prompt_tokens\":20,\"completion_tokens\":7,\"prompt_tokens_details\":{{\"cached_tokens\":5,\"cache_write_tokens\":2}}}}}}\n\ndata: [DONE]\n\n"
            ),
        );
        let (socket, request) = request(&model);
        let calls = &request["messages"][3]["tool_calls"];
        assert_eq!(request["messages"][3]["content"], "");
        assert_eq!(calls[0]["id"], "first");
        assert_eq!(calls[1]["id"], "second");
        assert_eq!(
            calls[0]["function"]["arguments"],
            r#"{"limit":1,"offset":1,"path":"Cargo.toml"}"#
        );
        for (index, id, content) in [(4, "first", "approved 1"), (5, "second", "approved 2")] {
            let tool = &request["messages"][index];
            assert_eq!(tool["role"], "tool");
            assert_eq!(tool["tool_call_id"], id);
            assert!(tool.get("name").is_none());
            assert!(tool.get("tool_name").is_none());
            let result: Value = serde_json::from_str(tool["content"].as_str().unwrap()).unwrap();
            assert_eq!(result["content"], content);
        }
        reply(
            socket,
            "text/event-stream",
            concat!(
                "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n",
                "data: {\"choices\":[{\"delta\":{\"content\":\"!\"},\"finish_reason\":\"stop\"}]}\n\n",
                "data: {\"usage\":{\"prompt_tokens\":37,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":8,\"cache_write_tokens\":3}}}\n\n",
                "data: [DONE]\n\n"
            ),
        );
    });

    let mut host = Host::spawn();
    host.begin("tools", &base_url, true);
    for id in ["first", "second"] {
        let started = host.next();
        assert_eq!(event(&started)["type"], "tool.start");
        assert_eq!(event(&started)["payload"]["tool_id"], id);
    }
    for (id, expected, approved) in [
        ("first", "1|[package]", "approved 1"),
        ("second", "2|name = \"lina-turn-host\"", "approved 2"),
    ] {
        let policy = host.next();
        assert_eq!(policy["type"], "read_file.policy");
        assert_eq!(policy["op"], "authorize");
        assert_eq!(policy["path"], "Cargo.toml");
        let mut ack = json!({
            "type": "read_file.policy.ack", "sid": "session", "request_id": policy["request_id"],
            "turn_id": "tools", "ok": true, "resolved": manifest, "display_path": "Cargo.toml",
            "identity": identity, "max_chars": 100_000, "max_lines": 2000, "max_line_length": 4000,
        });
        let mut stale = ack.clone();
        stale["turn_id"] = json!("previous-turn");
        stale["ok"] = json!(false);
        host.send(&stale);
        host.send(&ack);

        let redaction = host.next();
        assert_eq!(redaction["op"], "redact");
        assert_eq!(redaction["content"].as_str().unwrap().trim_end(), expected);
        ack["request_id"] = redaction["request_id"].clone();
        ack["content"] = json!(approved);
        host.send(&ack);
        let completed = host.next();
        assert_eq!(event(&completed)["type"], "tool.complete");
        assert_eq!(event(&completed)["payload"]["tool_id"], id);
        assert_eq!(event(&completed)["payload"]["result"]["content"], approved);
    }
    let first_delta = host.next();
    assert_eq!(event(&first_delta)["type"], "message.delta");
    assert_eq!(event(&first_delta)["payload"]["text"], "\n\nok");
    assert_eq!(event(&first_delta)["payload"]["offset"], 0);
    let next_delta = host.next();
    assert_eq!(event(&next_delta)["payload"]["text"], "!");
    assert_eq!(event(&next_delta)["payload"]["offset"], 4);
    let completion = host.next();
    assert_eq!(event(&completion)["type"], "message.complete");
    assert_eq!(event(&completion)["payload"]["text"], "ok!");
    let usage = &event(&completion)["payload"]["usage"];
    assert_eq!(usage["context_used"], 37);
    assert_eq!(usage["input"], 39);
    assert_eq!(usage["prompt"], 57);
    assert_eq!(usage["cache_read"], 13);
    assert_eq!(usage["cache_write"], 5);
    let end = host.next();
    assert_eq!(end["message_count"], 6);
    assert_eq!(end["messages"][5]["content"], "ok!");
    assert_eq!(end["messages"][5]["finish_reason"], "stop");
    assert_eq!(end["messages"][4]["tool_name"], "read_file");
    host.close();
    server.join().unwrap();
}

#[cfg(target_os = "linux")]
#[test]
fn interrupt_allows_already_authorized_read_to_finish_without_a_model_retry() {
    use std::os::unix::fs::MetadataExt;
    use std::path::Path;

    let manifest = Path::new(env!("CARGO_MANIFEST_DIR")).join("Cargo.toml");
    let metadata = manifest.metadata().unwrap();
    let identity = json!({
        "dev": metadata.dev(), "ino": metadata.ino(), "size": metadata.size(),
        "mtime_ns": metadata.mtime() * 1_000_000_000 + metadata.mtime_nsec(),
        "ctime_ns": metadata.ctime() * 1_000_000_000 + metadata.ctime_nsec(),
    });
    let (listener, base_url) = model();
    let (released, release) = mpsc::channel();
    let server = std::thread::spawn(move || {
        let (socket, _) = request(&listener);
        reply(
            socket,
            "application/json",
            r#"{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"reading","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"Cargo.toml\",\"limit\":1}"}}]}}]}"#,
        );
        release.recv_timeout(TIMEOUT).unwrap();
        listener.set_nonblocking(true).unwrap();
        assert_eq!(
            listener.accept().unwrap_err().kind(),
            std::io::ErrorKind::WouldBlock
        );
    });
    let mut host = Host::spawn();
    host.begin("tools", &base_url, false);
    assert_eq!(event(&host.next())["type"], "tool.start");
    let authorize = host.next();
    host.send(&json!({
        "type": "read_file.policy.ack", "sid": "session", "turn_id": "tools",
        "request_id": authorize["request_id"], "ok": true, "resolved": manifest,
        "display_path": "Cargo.toml", "identity": identity,
        "max_chars": 100_000, "max_lines": 2000, "max_line_length": 4000,
    }));
    let redact = host.next();
    assert_eq!(redact["op"], "redact");
    host.send(&json!({"type": "interrupt", "sid": "session", "request_id": "stop"}));
    assert_eq!(host.next()["applied"], true);
    host.send(&json!({
        "type": "read_file.policy.ack", "sid": "session", "turn_id": "tools",
        "request_id": redact["request_id"], "ok": true, "content": "approved",
    }));
    let complete = host.next();
    assert_eq!(event(&complete)["payload"]["result"]["content"], "approved");
    assert_eq!(event(&host.next())["payload"]["status"], "interrupted");
    let end = host.next();
    let persisted: Value =
        serde_json::from_str(end["new_messages"][2]["content"].as_str().unwrap()).unwrap();
    assert_eq!(persisted["content"], "approved");
    host.close();
    released.send(()).unwrap();
    server.join().unwrap();
}

#[test]
fn fifo_refusal_keeps_structured_event_and_ascii_escaped_model_content() {
    let (model, base_url) = model();
    let server = std::thread::spawn(move || {
        let (socket, _) = request(&model);
        let call = json!({"id":"fifo","type":"function","function":{
            "name":"read_file","arguments":json!({"path":"é😀"}).to_string()
        }});
        reply(
            socket,
            "application/json",
            &json!({"choices":[{"message":{"tool_calls":[call]}}]}).to_string(),
        );
        let (socket, request) = request(&model);
        let tool = request["messages"].as_array().unwrap().last().unwrap();
        assert_eq!(tool["tool_call_id"], "fifo");
        reply(
            socket,
            "application/json",
            r#"{"choices":[{"message":{"content":"done"}}]}"#,
        );
        tool["content"].as_str().unwrap().to_owned()
    });
    let mut host = Host::spawn();
    host.begin("fifo", &base_url, false);
    assert_eq!(event(&host.next())["type"], "tool.start");
    let policy = host.next();
    assert_eq!(policy["op"], "authorize");
    let refusal = json!({"success":false,"note":"'é😀' is a FIFO (named pipe) — reading refused."});
    host.send(
        &json!({"type":"read_file.policy.ack","sid":"session","turn_id":"fifo",
        "request_id":policy["request_id"],"ok":false,"result":refusal}),
    );
    let complete = host.next();
    assert_eq!(event(&complete)["type"], "tool.complete");
    assert_eq!(event(&complete)["payload"]["result"], refusal);
    while host.next()["type"] != "turn.end" {}
    host.close();
    assert_eq!(
        server.join().unwrap(),
        r#"{"success": false, "note": "'\u00e9\ud83d\ude00' is a FIFO (named pipe) \u2014 reading refused."}"#
    );
}
