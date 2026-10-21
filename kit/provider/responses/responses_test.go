// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func config(t *testing.T) Config {
	t.Helper()
	c, err := ReadConfig(strings.NewReader(`{"providers":{"chatgpt":{"adapter":"responses","retryCount":0,"requestFields":["model","input","store","stream","instructions","tools","reasoning","include"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func fixture(t *testing.T, c Config, h http.HandlerFunc) (*Adapter, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		if r.URL.Scheme != "https" || r.URL.Host != "api.openai.com" {
			t.Errorf("wrong endpoint: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("wrong credential")
		}
		clone := r.Clone(r.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return server.Client().Transport.RoundTrip(clone)
	})}
	a, err := New(c, client)
	if err != nil {
		t.Fatal(err)
	}
	return a, &count
}
func request() provider.Request {
	return provider.Request{Credential: provider.NewCredential("account", provider.LoopbackOAuth, "synthetic-token"), Model: "fixture-model", Purpose: provider.Turn, Items: []provider.Item{json.RawMessage(`{"role":"user","content":"hello"}`)}}
}
func run(t *testing.T, a *Adapter, r provider.Request) ([]provider.Event, provider.Meta, error) {
	t.Helper()
	l := lease.New(func() {})
	defer l.Release()
	s, err := a.Stream(t.Context(), l, r)
	if err != nil {
		return nil, provider.Meta{}, err
	}
	defer s.Close()
	var events []provider.Event
	for {
		e, err := s.Next(t.Context())
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return events, s.Meta(), err
		}
		events = append(events, e)
	}
}
func sse(w http.ResponseWriter, typ, fields string) {
	fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q,%s}\n\n", typ, typ, fields)
}
func completed(w http.ResponseWriter) {
	sse(w, "response.completed", `"response":{"id":"response-id","status":"completed"}`)
}
func failure(t *testing.T, err error, want provider.FailureKind) {
	t.Helper()
	var f *provider.Failure
	if !errors.As(err, &f) || f.Kind != want {
		t.Fatalf("failure %v, want %s", err, want)
	}
}

func TestFailurePrecedenceAndNoRetries(t *testing.T) {
	cases := []struct {
		kind      provider.FailureKind
		code, typ string
		status    int
	}{
		{provider.NeedsSignIn, "invalid_api_key", "authentication_error", 401},
		{provider.NotEligible, "not_eligible", "permission_error", 403},
		{provider.UsageCapped, "usage_limit_reached", "rate_limit_error", 429},
		{provider.Unavailable, "server_error", "server_error", 503},
		{provider.NotAccepted, "unsupported_parameter", "invalid_request_error", 400},
	}
	for _, tc := range cases {
		for _, level := range []string{"code", "type", "status"} {
			t.Run(string(tc.kind)+"/"+level, func(t *testing.T) {
				code, typ, status := tc.code, "unrecognized", 418
				if level == "code" {
					typ = "rate_limit_error"
					status = 503
				}
				if level == "type" {
					code = "unrecognized"
					typ = tc.typ
					status = 401
				}
				if level == "status" {
					code = "unrecognized"
					typ = "unrecognized"
					status = tc.status
				}
				a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("x-request-id", "request-id")
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":{"code":%q,"type":%q,"message":"authentication failed quota exhausted secret prompt"}}`, code, typ)
				})
				_, _, err := run(t, a, request())
				failure(t, err, tc.kind)
				if strings.Contains(err.Error(), "secret") || count.Load() != 1 {
					t.Fatalf("leak or retry: %v / %d", err, count.Load())
				}
				var f *provider.Failure
				errors.As(err, &f)
				if f.RequestID != "request-id" {
					t.Fatal("lost request id")
				}
			})
		}
	}
}

func TestCutOffOrFailedResponseEmitsNoTool(t *testing.T) {
	for _, ending := range []string{"eof", "incomplete", "failed", "error", "malformed"} {
		t.Run(ending, func(t *testing.T) {
			a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				sse(w, "response.output_text.delta", `"output_index":0,"delta":"hello"`)
				sse(w, "response.output_item.done", `"output_index":1,"item":{"type":"function_call","id":"item","call_id":"call","name":"effect","arguments":"{}"}`)
				switch ending {
				case "eof":
				case "incomplete":
					sse(w, "response.incomplete", `"response":{"status":"incomplete"}`)
				case "failed":
					sse(w, "response.failed", `"response":{"status":"failed","error":{"code":"usage_limit_reached"}}`)
				case "error":
					sse(w, "error", `"code":"server_error","message":"private"`)
				case "malformed":
					fmt.Fprint(w, "data: {broken\n\n")
				}
			})
			events, _, err := run(t, a, request())
			if err == nil {
				t.Fatal("accepted cut-off")
			}
			for _, e := range events {
				if e.Kind == provider.ToolCallReady {
					t.Fatal("cut-off tool dispatch")
				}
			}
			if count.Load() != 1 {
				t.Fatal("retried after output or failure")
			}
		})
	}
}

func TestOnlyDoneItemsAndCompletedResponseEnableTools(t *testing.T) {
	done := `{"type":"reasoning","id":"reason","encrypted_content":"final-opaque","summary":[],"future":{"keep":true}}`
	a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req")
		sse(w, "response.output_item.added", `"output_index":0,"item":{"type":"reasoning","encrypted_content":"partial"}`)
		sse(w, "response.function_call_arguments.delta", `"output_index":1,"delta":"partial"`)
		sse(w, "response.output_item.done", `"output_index":0,"item":`+done)
		sse(w, "response.output_item.done", `"output_index":1,"item":{"type":"function_call","id":"item","call_id":"call","name":"effect","arguments":"{}"}`)
		// Final response output cannot synthesize a missing done item.
		sse(w, "response.completed", `"response":{"status":"completed","output":[{"type":"function_call","call_id":"phantom","name":"bad","arguments":"{}"}],"usage":{"input_tokens":0,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`)
	})
	events, meta, err := run(t, a, request())
	if err != nil {
		t.Fatal(err)
	}
	var items []provider.Item
	var calls []provider.ToolCall
	for _, e := range events {
		if e.Kind == provider.ItemDone {
			items = append(items, e.Item)
		}
		if e.Kind == provider.ToolCallReady {
			calls = append(calls, *e.Call)
		}
	}
	if len(items) != 2 || string(items[0]) != done || len(calls) != 1 || calls[0].ID != "call" || calls[0].Arguments != "{}" {
		t.Fatalf("assembly: %+v", events)
	}
	if meta.RequestID != "req" || meta.Usage.Input == nil || *meta.Usage.Input != 0 || meta.Usage.Output == nil || *meta.Usage.Output != 2 || meta.Usage.Cached == nil || meta.Usage.Reasoning != nil || meta.Usage.CostEstimate != nil || count.Load() != 1 {
		t.Fatalf("metadata: %+v", meta)
	}
}

func TestFailedToolInputDeltasAreNotVisibleText(t *testing.T) {
	a, _ := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, "response.output_text.delta", `"output_index":0,"delta":"visible"`)
		sse(w, "response.function_call_arguments.delta", `"output_index":1,"delta":"function arguments"`)
		sse(w, "response.custom_tool_call_input.delta", `"output_index":2,"delta":"custom input"`)
		// EOF before any done item or completed response.
	})
	events, _, err := run(t, a, request())
	if err == nil {
		t.Fatal("truncated response succeeded")
	}
	if len(events) != 1 || events[0].Kind != provider.Delta || events[0].Index != 0 || events[0].Delta != "visible" {
		t.Fatalf("only output_text may enter visible partial history: %+v", events)
	}
}

func TestManifestFieldsAndOpaqueInput(t *testing.T) {
	c := config(t)
	c.RequestFields = slices.DeleteFunc(c.RequestFields, func(s string) bool { return s == "instructions" })
	item := json.RawMessage(`{"type":"reasoning","id":"reason","encrypted_content":"opaque","summary":[],"future":{"value":1}}`)
	a, _ := fixture(t, c, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		for key := range body {
			if !slices.Contains(c.RequestFields, key) {
				t.Errorf("unlisted field %s: %s", key, raw)
			}
		}
		if string(body["store"]) != "false" || string(body["stream"]) != "true" || string(body["include"]) != `["reasoning.encrypted_content"]` {
			t.Errorf("wrong controls: %s", raw)
		}
		var items []json.RawMessage
		json.Unmarshal(body["input"], &items)
		if len(items) != 2 || string(items[1]) != string(item) {
			t.Errorf("changed input: %s", raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w)
	})
	r := request()
	r.Items = append(r.Items, item)
	r.Instructions = "not allowed"
	r.Effort = "high"
	r.Fields = append(c.RequestFields, "metadata", "instructions")
	_, meta, err := run(t, a, r)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Usage.Input != nil || meta.Usage.Output != nil {
		t.Fatal("invented usage")
	}
}

func TestEmptyEffortStillRequestsEncryptedReasoning(t *testing.T) {
	a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["store"]) != "false" || string(body["include"]) != `["reasoning.encrypted_content"]` {
			t.Errorf("stateless reasoning request lost encrypted state: store=%s include=%s", body["store"], body["include"])
		}
		if _, ok := body["reasoning"]; ok {
			t.Error("empty effort must retain the model default")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w)
	})
	r := request()
	r.Model = "fixture-reasoning-model"
	if _, _, err := run(t, a, r); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatalf("requests: %d", count.Load())
	}
}

func TestEncryptedReasoningMustBeAllowedWithoutEffort(t *testing.T) {
	for _, source := range []string{"manifest", "request"} {
		t.Run(source, func(t *testing.T) {
			c := config(t)
			fields := slices.DeleteFunc(slices.Clone(c.RequestFields), func(field string) bool { return field == "include" })
			r := request()
			if source == "manifest" {
				c.RequestFields = fields
			} else {
				r.Fields = fields
			}
			a, count := fixture(t, c, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				completed(w)
			})
			_, _, err := run(t, a, r)
			failure(t, err, provider.NotAccepted)
			if count.Load() != 0 {
				t.Fatal("sent stateless request without encrypted state")
			}
		})
	}
}

func TestManifestRejectsUnsafeConfiguration(t *testing.T) {
	for _, bad := range []string{
		`{}`, `{"providers":{"chatgpt":{}}}`,
		`{"providers":{"chatgpt":{"adapter":"responses","requestFields":["model","input","store","stream"]}}}`,
	} {
		if _, err := ReadConfig(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	for _, field := range []string{"previous_response_id", "metadata", "max_output_tokens", "temperature", "truncation", "user"} {
		c := config(t)
		c.RequestFields = append(c.RequestFields, field)
		if _, err := New(c, nil); err == nil {
			t.Fatalf("accepted forbidden %s", field)
		}
	}
	c := config(t)
	c.RetryCount = 1
	if _, err := New(c, nil); err == nil {
		t.Fatal("accepted retries")
	}
	c = config(t)
	c.RequestFields = []string{"model", "input", "stream"}
	if _, err := New(c, nil); err == nil {
		t.Fatal("accepted missing store")
	}
}

func TestCancellationReleasesLeaseWithoutRetry(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	})
	released := make(chan struct{})
	l := lease.New(func() { close(released) })
	s, err := a.Stream(t.Context(), l, request())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	l.Release()
	select {
	case <-released:
		t.Fatal("lease ended while stream live")
	default:
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-t.Context().Done():
		t.Fatal("lease not released")
	}
	select {
	case <-stopped:
	case <-t.Context().Done():
		t.Fatal("request not cancelled")
	}
	if count.Load() != 1 {
		t.Fatal("retry")
	}
}

func TestNoRequestWithoutLeaseOrAfterCancellation(t *testing.T) {
	a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) { completed(w) })
	if _, err := a.Stream(t.Context(), lease.Lease{}, request()); err == nil {
		t.Fatal("missing lease allowed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l := lease.New(func() {})
	defer l.Release()
	if _, err := a.Stream(ctx, l, request()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if count.Load() != 0 {
		t.Fatal("sent without live lease")
	}
}

func TestOpaqueInputBytesAndToolOrder(t *testing.T) {
	item := json.RawMessage(`{ "type": "reasoning", "encrypted_content": "opaque<>&", "summary": [] }`)
	tools := []provider.ToolDecl{
		json.RawMessage(`{"type":"custom","name":"apply_patch","description":"Apply patch"}`),
		json.RawMessage(`{"type":"function","name":"read","parameters":{"type":"object","properties":{}}}`),
	}
	a, _ := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), string(item)) {
			t.Errorf("rewrote opaque input: %s", raw)
		}
		var body struct{ Tools []json.RawMessage }
		json.Unmarshal(raw, &body)
		if len(body.Tools) != 2 || string(body.Tools[0]) != string(tools[0]) || string(body.Tools[1]) != string(tools[1]) {
			t.Error("tool order or declarations changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w)
	})
	r := request()
	r.Items = append(r.Items, item)
	r.Tools = tools
	if _, _, err := run(t, a, r); err != nil {
		t.Fatal(err)
	}
}

func TestStreamFailureCodesAndTypes(t *testing.T) {
	for _, tc := range []struct {
		code, typ string
		want      provider.FailureKind
	}{
		{"invalid_api_key", "authentication_error", provider.NeedsSignIn},
		{"not_eligible", "permission_error", provider.NotEligible},
		{"usage_limit_reached", "rate_limit_error", provider.UsageCapped},
		{"server_error", "server_error", provider.Unavailable},
		{"unsupported_parameter", "invalid_request_error", provider.NotAccepted},
	} {
		for _, level := range []string{"code", "type"} {
			t.Run(tc.code+"/"+level, func(t *testing.T) {
				code, typ := tc.code, "authentication_error"
				if level == "type" {
					code = "unknown"
					typ = tc.typ
				}
				a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					sse(w, "response.failed", fmt.Sprintf(`"response":{"status":"failed","error":{"code":%q,"type":%q}}`, code, typ))
				})
				_, _, err := run(t, a, request())
				failure(t, err, tc.want)
				if count.Load() != 1 {
					t.Fatal("retry")
				}
			})
		}
	}
}

func TestRedirectAndTransportFailureNeverResend(t *testing.T) {
	for _, status := range []int{307, 308, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://api.openai.com/v1/responses")
				w.WriteHeader(status)
			})
			_, _, err := run(t, a, request())
			if err == nil || count.Load() != 1 {
				t.Fatalf("redirect/retry: %v, %d", err, count.Load())
			}
		})
	}
	var calls atomic.Int64
	a, err := New(config(t), &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("private transport details")
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = run(t, a, request())
	failure(t, err, provider.Unavailable)
	if calls.Load() != 1 {
		t.Fatal("connection retried")
	}
}

func TestApplyPatchFunctionMustTakeOneString(t *testing.T) {
	a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		completed(w)
	})
	r := request()
	r.Tools = []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"path":{"type":"string"},"patch":{"type":"string"}}}}`)}
	_, _, err := run(t, a, r)
	failure(t, err, provider.NotAccepted)
	if count.Load() != 0 {
		t.Fatal("unsupported apply_patch sent")
	}
	r.Tools = []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}}`)}
	if _, _, err := run(t, a, r); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaModelsAndMetadataSnapshot(t *testing.T) {
	a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"listed-model"}],"object":"list"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, "response.completed", `"response":{"status":"completed","usage":{"input_tokens":2}}`)
	})
	l := lease.New(func() {})
	defer l.Release()
	models, err := a.Models(t.Context(), l, request().Credential)
	if err != nil || len(models.Models) != 1 || models.Models[0] != "listed-model" {
		t.Fatalf("models: %+v %v", models, err)
	}
	rows, err := a.Quota(t.Context(), request().Credential)
	if err != nil || len(rows) != 1 || rows[0].UsedPct != nil || rows[0].ResetsAt != nil || rows[0].State != provider.QuotaUnknown || count.Load() != 1 {
		t.Fatalf("quota: %+v %v", rows, err)
	}
	s, err := a.Stream(t.Context(), l, request())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		_, err := s.Next(t.Context())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	m := s.Meta()
	*m.Usage.Input = 999
	if *s.Meta().Usage.Input != 2 {
		t.Fatal("metadata shares mutable usage")
	}
	for range 2 {
		if _, err := s.Next(t.Context()); err != io.EOF {
			t.Fatal(err)
		}
	}
	if count.Load() != 2 {
		t.Fatal("request restarted")
	}
}

func TestSSEFramingAndLargeDelta(t *testing.T) {
	delta := strings.Repeat("x", 128<<10)
	a, _ := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(w, ": keepalive\r\n\r\nid: ignored\r\nevent: response.output_text.delta\r\n")
		fmt.Fprintf(w, "data: {\r\ndata: \"output_index\":0,\"delta\":%q}\r\n\r\n", delta)
		completed(w)
	})
	events, _, err := run(t, a, request())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != provider.Delta || events[0].Delta != delta || events[1].Kind != provider.Terminal {
		t.Fatal("lost framed event or large delta")
	}
}

func TestSSECutOffAndNestedError(t *testing.T) {
	for _, wire := range []string{
		"data: [DONE]\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}",
		"event: error\ndata: {\"error\":{\"code\":\"server_error\",\"message\":\"private\"}}\n\n",
	} {
		a, count := fixture(t, config(t), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, wire)
		})
		events, _, err := run(t, a, request())
		failure(t, err, provider.Unavailable)
		if len(events) != 0 || count.Load() != 1 {
			t.Fatal("accepted incomplete response or retried")
		}
	}
}
