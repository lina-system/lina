// SPDX-License-Identifier: Apache-2.0
package messages

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
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func config(t *testing.T) Config {
	t.Helper()
	c, err := ReadConfig(strings.NewReader(`{"providers":{"claude_api_key":{"adapter":"messages","retryCount":0,"maxOutputTokens":4096,"requestFields":["model","max_tokens","stream","system","messages","tools","thinking"],"capabilities":["prefix_cache","cache_marks","cache_lifetime_1h","cache_lifetime_5m","reasoning_state","batch"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fixture redirects the fixed public endpoint to a loopback server and counts
// every request that reaches it. The credential is checked at the boundary.
func fixture(t *testing.T, c Config, h http.Handler) (*Adapter, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		if r.URL.Scheme != "https" || r.URL.Host != "api.anthropic.com" {
			t.Errorf("wrong endpoint: %s", r.URL)
		}
		if r.Header.Get("x-api-key") != "synthetic-key" {
			t.Error("wrong credential header")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing API version")
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

func message(role, text string) provider.Item {
	b, _ := json.Marshal(map[string]string{"role": role, "content": text})
	return b
}
func request() provider.Request {
	return provider.Request{Credential: provider.NewCredential("claude-api", provider.APIKey, "synthetic-key"), Model: "fixture-model", Purpose: provider.Turn, Items: []provider.Item{message("user", "hello")}}
}
func collect(t *testing.T, a *Adapter, r provider.Request) ([]provider.Event, provider.Meta, error) {
	t.Helper()
	return collectContext(t, a, t.Context(), r)
}

func collectContext(t *testing.T, a *Adapter, ctx context.Context, r provider.Request) ([]provider.Event, provider.Meta, error) {
	t.Helper()
	l := lease.New(func() {})
	defer l.Release()
	s, err := a.Stream(ctx, l, r)
	if err != nil {
		return nil, provider.Meta{}, err
	}
	defer s.Close()
	var events []provider.Event
	for {
		e, err := s.Next(context.WithoutCancel(ctx))
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return events, s.Meta(), err
		}
		events = append(events, e)
	}
}
func event(w http.ResponseWriter, typ, fields string) {
	fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q%s}\n\n", typ, typ, fields)
}
func streamHeaders(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/event-stream") }
func failure(t *testing.T, err error, want provider.FailureKind) {
	t.Helper()
	var f *provider.Failure
	if !errors.As(err, &f) || f.Kind != want {
		t.Fatalf("failure %v, want %s", err, want)
	}
}
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

// A thinking block and its signature come back in the next request unchanged,
// inside a tool loop, and the adapter reports reads and writes by lifetime.
func TestThinkingAndToolLoopRoundTrip(t *testing.T) {
	var bodies []map[string]any
	a, _ := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, decodeBody(t, r))
		streamHeaders(w)
		event(w, "message_start", `,"message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":12,"cache_read_input_tokens":30,"cache_creation_input_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":7},"output_tokens":1}}`)
		event(w, "content_block_start", `,"index":0,"content_block":{"type":"thinking","thinking":"","signature":""}`)
		event(w, "content_block_delta", `,"index":0,"delta":{"type":"thinking_delta","thinking":"consider"}`)
		event(w, "content_block_delta", `,"index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}`)
		event(w, "content_block_stop", `,"index":0`)
		event(w, "content_block_start", `,"index":1,"content_block":{"type":"text","text":""}`)
		event(w, "content_block_delta", `,"index":1,"delta":{"type":"text_delta","text":"Hello"}`)
		event(w, "content_block_stop", `,"index":1`)
		event(w, "content_block_start", `,"index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}`)
		event(w, "content_block_delta", `,"index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}`)
		event(w, "content_block_stop", `,"index":2`)
		event(w, "message_delta", `,"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}`)
		event(w, "message_stop", "")
	}))
	events, meta, err := collect(t, a, request())
	if err != nil {
		t.Fatal(err)
	}
	var thinking, text, call provider.Item
	for _, e := range events {
		switch e.Kind {
		case provider.Delta:
			if e.Delta != "Hello" {
				t.Fatalf("unexpected delta %q", e.Delta)
			}
		case provider.ItemDone:
			var v struct{ Type, Role string }
			_ = json.Unmarshal(e.Item, &v)
			switch v.Type {
			case "reasoning":
				thinking = e.Item
			case "message":
				text = e.Item
			case "function_call":
				call = e.Item
			}
		}
	}
	if thinking == nil || text == nil || call == nil {
		t.Fatalf("missing final items: %v", events)
	}
	var block struct {
		Type, Thinking, Signature string
	}
	if json.Unmarshal(thinking, &block) != nil || block.Thinking != "consider" || block.Signature != "sig-abc" {
		t.Fatalf("thinking lost: %s", thinking)
	}
	if !strings.Contains(string(call), `"call_id":"toolu_1"`) || !strings.Contains(string(call), `"arguments":"{\"path\":\"a\"}"`) {
		t.Fatalf("tool call shape: %s", call)
	}
	if meta.Usage.Input == nil || *meta.Usage.Input != 12 || meta.Usage.Cached == nil || *meta.Usage.Cached != 30 || meta.Usage.CacheWrite == nil || *meta.Usage.CacheWrite != 7 || meta.Usage.Output == nil || *meta.Usage.Output != 9 {
		t.Fatalf("usage %+v", meta.Usage)
	}
	if write := meta.Usage.CacheByLifetime[provider.Cache1Hour].Write; write == nil || *write != 7 {
		t.Fatalf("lifetime writes %+v", meta.Usage.CacheByLifetime)
	}

	// The tool loop returns the final items plus a result, exactly as the loop does.
	follow := request()
	follow.Items = []provider.Item{
		message("user", "hello"),
		thinking,
		text,
		call,
		provider.Item(`{"type":"function_call_output","call_id":"toolu_1","output":"file body"}`),
	}
	_, _, err = collect(t, a, follow)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests %d", len(bodies))
	}
	sent := decodeMessages(t, bodies[1])
	if len(sent) != 3 || sent[0].Role != "user" || sent[1].Role != "assistant" || sent[2].Role != "user" {
		t.Fatalf("message roles: %+v", sent)
	}
	assistant := sent[1].Content
	if len(assistant) != 3 || assistant[0].Type != "thinking" || assistant[0].Thinking != "consider" || assistant[0].Signature != "sig-abc" {
		t.Fatalf("thinking not returned unchanged: %+v", assistant)
	}
	if assistant[1].Type != "text" || assistant[1].Text != "Hello" {
		t.Fatalf("assistant text: %+v", assistant)
	}
	if assistant[2].Type != "tool_use" || assistant[2].ID != "toolu_1" || string(assistant[2].Input) != `{"path":"a"}` {
		t.Fatalf("tool use not returned: %+v", assistant)
	}
	if result := sent[2].Content; len(result) != 1 || result[0].Type != "tool_result" || result[0].ToolUseID != "toolu_1" || string(result[0].Content) != `"file body"` {
		t.Fatalf("tool result: %+v", result)
	}
}

// Marks land on the content blocks at the boundaries Core selected, once each,
// with the 1-hour lifetime the route admits.
func TestCacheMarksAtBoundaries(t *testing.T) {
	var body map[string]any
	a, _ := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		streamHeaders(w)
		event(w, "message_start", `,"message":{"id":"msg_1","role":"assistant","content":[]}`)
		event(w, "content_block_start", `,"index":0,"content_block":{"type":"text","text":""}`)
		event(w, "content_block_stop", `,"index":0`)
		event(w, "message_stop", "")
	}))
	position := func(n int) *provider.Position { return &provider.Position{Items: n} }
	r := request()
	r.Instructions = "core"
	r.Items = []provider.Item{
		message("developer", "catalog"),
		message("user", "earlier question"),
		message("assistant", "earlier answer"),
		message("user", "current question"),
		message("developer", "context"),
	}
	r.Layout = &provider.RequestLayout{Fixed: provider.Position{Items: 1}, PreviousInput: position(2), CurrentInput: provider.Position{Items: 4}}
	for _, p := range []provider.Position{{Items: 1}, {Items: 2}, {Items: 4}} {
		r.Capabilities.CacheMarks = append(r.Capabilities.CacheMarks, provider.CacheMark{Position: p, Lifetime: provider.Cache1Hour})
	}
	if _, _, err := collect(t, a, r); err != nil {
		t.Fatal(err)
	}
	sent := decodeMessages(t, body)
	marked := map[string]string{}
	for _, m := range sent {
		for _, b := range m.Content {
			if b.CacheControl == nil {
				continue
			}
			if b.CacheControl.Type != "ephemeral" || b.CacheControl.TTL != "1h" {
				t.Fatalf("wrong mark %+v", b.CacheControl)
			}
			marked[b.Text] = b.CacheControl.TTL
		}
	}
	if len(marked) != 3 || marked["catalog"] != "1h" || marked["earlier question"] != "1h" || marked["current question"] != "1h" {
		t.Fatalf("marks not at the selected boundaries: %v", marked)
	}
	if _, ok := marked["context"]; ok {
		t.Fatal("mark on the newest block")
	}

	// A mark outside the layout, a fifth mark and a foreign lifetime never
	// reach the wire.
	for _, change := range []func(*provider.Request){
		func(r *provider.Request) { r.Capabilities.CacheMarks[0].Position = provider.Position{Items: 3} },
		func(r *provider.Request) {
			r.Capabilities.CacheMarks = append(r.Capabilities.CacheMarks, provider.CacheMark{Position: provider.Position{Items: 5}}, provider.CacheMark{Position: provider.Position{Items: 4}})
		},
		func(r *provider.Request) { r.Capabilities.CacheMarks[0].Lifetime = "2h" },
	} {
		bad := r
		bad.Capabilities = r.Capabilities.Clone()
		change(&bad)
		_, _, err := collect(t, a, bad)
		failure(t, err, provider.NotAccepted)
	}
}

// A missing or refused key reports needs sign-in after one request and never
// moves to another account or another route.
func TestMissingAndRefusedKey(t *testing.T) {
	a, count := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	r := request()
	r.Credential = provider.NewCredential("claude-api", provider.APIKey, "")
	_, _, err := collect(t, a, r)
	failure(t, err, provider.NeedsSignIn)
	if count.Load() != 0 {
		t.Fatal("request without a key reached the provider")
	}
	_, _, err = collect(t, a, request())
	failure(t, err, provider.NeedsSignIn)
	if count.Load() != 1 {
		t.Fatalf("requests %d, want no retry", count.Load())
	}
	r = request()
	r.Credential = provider.NewCredential("claude-api", provider.LongLivedToken, "synthetic-key")
	_, _, err = collect(t, a, r)
	failure(t, err, provider.NotAccepted)
}

func TestFailurePrecedence(t *testing.T) {
	cases := []struct {
		kind      provider.FailureKind
		code, typ string
		status    int
	}{
		{provider.NeedsSignIn, "invalid_api_key", "authentication_error", 401},
		{provider.NotEligible, "permission_error", "permission_error", 403},
		{provider.UsageCapped, "rate_limit_error", "rate_limit_error", 429},
		{provider.Unavailable, "overloaded_error", "overloaded_error", 529},
		{provider.NotAccepted, "invalid_request_error", "invalid_request_error", 400},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			a, _ := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"type":"error","error":{"type":"%s","message":"refused"}}`, tc.typ)
			}))
			_, _, err := collect(t, a, request())
			failure(t, err, tc.kind)
			var f *provider.Failure
			errors.As(err, &f)
			if f.Status != tc.status || f.Type != tc.typ {
				t.Fatalf("failure %+v", f)
			}
		})
	}
	t.Run("stream error", func(t *testing.T) {
		a, _ := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			streamHeaders(w)
			event(w, "error", `,"error":{"type":"overloaded_error","message":"busy"}`)
		}))
		_, _, err := collect(t, a, request())
		failure(t, err, provider.Unavailable)
	})
	t.Run("cut off", func(t *testing.T) {
		a, _ := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			streamHeaders(w)
			event(w, "message_start", `,"message":{"id":"msg_1","role":"assistant","content":[]}`)
		}))
		_, _, err := collect(t, a, request())
		failure(t, err, provider.Unavailable)
	})
}

// A background job past the batch window runs as a batch; an expired batch runs
// once as a normal request.
func TestBatchForBackgroundJob(t *testing.T) {
	var normal atomic.Int64
	batches := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/batches":
			body := decodeBody(t, r)
			requests := body["requests"].([]any)
			params := requests[0].(map[string]any)["params"].(map[string]any)
			if params["stream"] != nil || params["model"] == nil || params["max_tokens"] == nil {
				t.Errorf("batch params %v", params)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"batch_1","processing_status":"in_progress","request_counts":{"processing":1}}`)
		case r.URL.Path == "/v1/messages/batches/batch_1":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"batch_1","processing_status":"ended","request_counts":{"succeeded":1}}`)
		case r.URL.Path == "/v1/messages/batches/batch_1/results":
			w.Header().Set("Content-Type", "application/x-jsonl")
			fmt.Fprint(w, `{"custom_id":"lina","result":{"type":"succeeded","message":{"id":"msg_2","role":"assistant","content":[{"type":"text","text":"batched"}],"usage":{"input_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":2}}}}`+"\n")
		case r.URL.Path == "/v1/messages":
			normal.Add(1)
			streamHeaders(w)
			event(w, "message_start", `,"message":{"id":"msg_3","role":"assistant","content":[]}`)
			event(w, "content_block_start", `,"index":0,"content_block":{"type":"text","text":""}`)
			event(w, "content_block_delta", `,"index":0,"delta":{"type":"text_delta","text":"plain"}`)
			event(w, "content_block_stop", `,"index":0`)
			event(w, "message_stop", "")
		default:
			t.Errorf("unexpected batch request %s %s", r.Method, r.URL.Path)
		}
	})
	a, _ := fixture(t, config(t), batches)
	job := request()
	job.Purpose = provider.BackgroundJob
	// A job whose deadline lies beyond the batch window may defer.
	batchCtx, batchCancel := context.WithTimeout(t.Context(), 30*time.Hour)
	defer batchCancel()
	events, meta, err := collectContext(t, a, batchCtx, job)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Batch != "batch_1" || len(events) == 0 || events[len(events)-1].Kind != provider.Terminal {
		t.Fatalf("batch result %v %+v", meta, events)
	}
	if normal.Load() != 0 {
		t.Fatal("a served batch also ran as a request")
	}

	expired := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/messages/batches":
			fmt.Fprint(w, `{"id":"batch_2","processing_status":"in_progress"}`)
		case "/v1/messages/batches/batch_2":
			fmt.Fprint(w, `{"id":"batch_2","processing_status":"ended"}`)
		case "/v1/messages/batches/batch_2/results":
			fmt.Fprint(w, `{"custom_id":"lina","result":{"type":"expired"}}`+"\n")
		case "/v1/messages":
			normal.Add(1)
			streamHeaders(w)
			event(w, "message_start", `,"message":{"id":"msg_4","role":"assistant","content":[]}`)
			event(w, "content_block_start", `,"index":0,"content_block":{"type":"text","text":""}`)
			event(w, "content_block_stop", `,"index":0`)
			event(w, "message_stop", "")
		}
	})
	a, _ = fixture(t, config(t), expired)
	events, meta, err = collectContext(t, a, batchCtx, job)
	if err != nil || meta.Batch != "" || normal.Load() != 1 {
		t.Fatalf("expired batch %v %+v %d", err, meta, normal.Load())
	}
	if len(events) == 0 || events[len(events)-1].Kind != provider.Terminal {
		t.Fatal("fallback request did not finish")
	}

	// A job whose deadline is inside the batch window goes straight to a request.
	a, _ = fixture(t, config(t), expired)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	l := lease.New(func() {})
	defer l.Release()
	s, err := a.Stream(ctx, l, job)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if normal.Load() != 2 {
		t.Fatalf("short deadline used a batch: %d", normal.Load())
	}
}

func TestModelsQuotaAndBudget(t *testing.T) {
	a, _ := fixture(t, config(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"claude-fixture-1","display_name":"Fixture"}],"has_more":false}`)
	}))
	l := lease.New(func() {})
	defer l.Release()
	list, err := a.Models(t.Context(), l, request().Credential)
	if err != nil || len(list.Models) != 1 || list.Models[0] != "claude-fixture-1" || list.Observed.IsZero() {
		t.Fatalf("models %+v %v", list, err)
	}
	rows, err := a.Quota(t.Context(), request().Credential)
	if err != nil || len(rows) != 1 || rows[0].Window != provider.UnknownWindow || rows[0].State != provider.QuotaUnknown {
		t.Fatalf("quota %+v %v", rows, err)
	}
	window, count := a.RequestBudget("fixture-model")
	if window != 128000 || count(request()) <= 0 {
		t.Fatalf("budget %d %d", window, count(request()))
	}
}

func TestBodyRefusesUnsupportedInput(t *testing.T) {
	a, count := fixture(t, config(t), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unsupported request reached the provider")
	}))
	for _, change := range []func(*provider.Request){
		func(r *provider.Request) {
			r.Items = []provider.Item{provider.Item(`{"type":"custom_tool_call","call_id":"c","name":"apply_patch"}`)}
		},
		func(r *provider.Request) { r.Items = []provider.Item{provider.Item(`{"type":"unknown"}`)} },
		func(r *provider.Request) { r.Items = []provider.Item{provider.Item(`{"role":"system","content":"x"}`)} },
		func(r *provider.Request) {
			r.Tools = []provider.ToolDecl{provider.ToolDecl(`{"type":"custom","name":"apply_patch"}`)}
		},
		func(r *provider.Request) {
			r.Tools = []provider.ToolDecl{provider.ToolDecl(`{"type":"function","name":""}`)}
		},
		func(r *provider.Request) { r.Model = "" },
		func(r *provider.Request) { r.Purpose = "unknown" },
		func(r *provider.Request) { r.Fields = provider.FieldList{"model"} },
		func(r *provider.Request) { r.Capabilities.ServerState = "handle" },
	} {
		r := request()
		change(&r)
		_, _, err := collect(t, a, r)
		failure(t, err, provider.NotAccepted)
	}
	if count.Load() != 0 {
		t.Fatalf("refused requests reached the provider: %d", count.Load())
	}
	if !slices.Contains(a.Capabilities("fixture-model"), provider.CacheMarksCapability) {
		t.Fatal("marks not declared")
	}
}

type wireBlockView struct {
	CacheControl *struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	} `json:"cache_control"`
	Type      string          `json:"type"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

type wireMessageView struct {
	Role    string          `json:"role"`
	Content []wireBlockView `json:"content"`
}

// decodeMessages reads the wire messages from a decoded request body.
func decodeMessages(t *testing.T, body map[string]any) []wireMessageView {
	t.Helper()
	raw, err := json.Marshal(body["messages"])
	if err != nil {
		t.Fatal(err)
	}
	var messages []wireMessageView
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	return messages
}
