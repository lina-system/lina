// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// TestResponsesRoundTrip/fixture and /real run the same Core-side journey
// through internal/core/modelpath.ResponsesFromEnv and the Responses adapter:
// send a synthetic user message, observe nonempty live deltas, receive
// completed assistant items and a terminal event, release the model lease, and
// save and reread a private probe record in a temporary state directory. Only
// final items are saved; deltas are display data. The probe record is a test
// artifact, not a canon turn. Output contains event counts and outcomes, never
// the endpoint, key or model reply. The test removes its state on completion,
// including failure.
//
// The same Core-side journey exercises the fixture and the explicitly selected
// real model. It uses no client conversation implementation or substitute model.
func TestResponsesRoundTrip(t *testing.T) {
	t.Run("fixture", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
				t.Error("wrong configured request")
				w.WriteHeader(400)
				return
			}
			var request struct {
				Model  string
				Input  []provider.Item
				Store  bool
				Stream bool
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "fixture-model" || len(request.Input) != 1 || request.Store || !request.Stream {
				t.Error("invalid stateless request")
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Hello\"}\n\n")
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello!\"}]}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":2}}}\n\n")
		}))
		defer server.Close()
		env := map[string]string{"LINA_RESPONSES_BASE_URL": server.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}
		responsesRoundTrip(t, func(name string) string { return env[name] })
	})
	t.Run("real", func(t *testing.T) {
		requireRealModel(t)
		responsesRoundTrip(t, os.Getenv)
	})
}

func requireRealModel(t *testing.T) {
	t.Helper()
	if os.Getenv("LINA_QA_REAL_MODEL") != "1" || os.Getenv("LINA_RESPONSES_API_KEY") == "" {
		t.Skip("real model requires LINA_QA_REAL_MODEL=1 and LINA_RESPONSES_API_KEY")
	}
	// Opting in with a key but an incomplete/invalid endpoint is a failure, not a
	// skip that could hide a broken acceptance environment.
	if cfg, err := modelpath.ResponsesFromEnv(os.Getenv); err != nil || cfg == nil {
		t.Fatalf("real model configuration: %v", err)
	}
}

func responsesRoundTrip(t *testing.T, getenv func(string) string) {
	t.Helper()
	cfg, err := modelpath.ResponsesFromEnv(getenv)
	if err != nil || cfg == nil {
		t.Fatalf("Core provider configuration: %v", err)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	registry := lifecycle.NewRegistry(nil)
	l, err := registry.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "QA Responses round trip"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	input := json.RawMessage(`{"role":"user","content":"Reply with a short greeting."}`)
	stream, err := cfg.Adapter.Stream(ctx, l, cfg.Request([]provider.Item{input}))
	if err != nil {
		t.Fatalf("Responses start: %v", err)
	}
	defer stream.Close()
	var record struct {
		Model provider.ModelRef `json:"model"`
		Input provider.Item     `json:"input"`
		Items []provider.Item   `json:"items"`
		Usage provider.Usage    `json:"usage"`
	}
	record.Model, record.Input = cfg.Model, input
	deltas, terminal := 0, false
	for {
		event, err := stream.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Responses stream: %v", err)
		}
		switch event.Kind {
		case provider.Delta:
			if terminal {
				t.Fatal("delta after terminal")
			}
			if event.Delta != "" {
				deltas++
			}
		case provider.ItemDone:
			if terminal {
				t.Fatal("item after terminal")
			}
			record.Items = append(record.Items, event.Item)
		case provider.ToolCallReady:
			t.Fatal("unexpected tool call with no tools declared")
		case provider.Terminal:
			if terminal {
				t.Fatal("duplicate terminal")
			}
			terminal = true
		}
	}
	if deltas == 0 || !terminal || len(record.Items) == 0 {
		t.Fatalf("incomplete round trip: deltas=%d items=%d terminal=%t", deltas, len(record.Items), terminal)
	}
	var answer strings.Builder
	for _, raw := range record.Items {
		var item struct {
			Type, Role string
			Content    []struct{ Type, Text string }
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal("invalid final item")
		}
		if item.Type == "message" && item.Role == "assistant" {
			for _, content := range item.Content {
				if content.Type == "output_text" {
					answer.WriteString(content.Text)
				}
			}
		}
	}
	if strings.TrimSpace(answer.String()) == "" {
		t.Fatal("no final assistant text")
	}
	record.Usage = stream.Meta().Usage
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if registry.Idle(ctx).Busy() {
		t.Fatal("model lease remained live after stream closed")
	}
	// This is a temporary QA artifact, not a canon conversation turn. Persist only
	// final provider items, never transient deltas, endpoint settings or credentials.
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "responses-roundtrip.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(data) {
		t.Fatal("temporary probe record did not round trip")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("probe record is not private")
	}
	t.Logf("Core-side Responses round trip: deltas=%d final_items=%d terminal=true saved_probe=true; client/canon turn not exercised", deltas, len(record.Items))
}

// TestRealModelHeadlessConversation installs the built binaries in private
// temporary state and starts actual Core with the configured route. A typed
// headless client negotiates conversation over Core's Unix socket, discovers
// and subscribes to the conversation, and sends one synthetic greeting request.
// It requires durable input acknowledgement, nonempty live deltas, completed
// assistant items replacing the streamed identities, and an inactive turn. It
// then stops and restarts Core and compares restored canon history with the
// live input and reply, including text, order and item identities, under the
// same conversation id and a new Core epoch. Output reports only counts and
// outcomes, never configuration or reply text. The model call and restart have
// a two-minute deadline; cleanup removes the installation, canon and child
// processes, including on failure. Installation is Linux-only, so other hosts
// skip before any configuration or model work.
func TestRealModelHeadlessConversation(t *testing.T) {
	requireLinuxCoreInstall(t)
	requireRealModel(t)
	headlessConversationRoundTrip(t, os.Getenv, nil)
}

// requireLinuxCoreInstall reports the hosts that cannot install Core as a
// skip. This scenario installs real binaries and is not portable.
func requireLinuxCoreInstall(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux Core installation")
	}
}

const headlessQAInput = "Reply with a short greeting."

// TestHeadlessConversationFixture runs the TestRealModelHeadlessConversation
// client journey without external credentials. Its loopback Responses server
// waits for the client to receive the first delta before completing the reply,
// proving streaming without timing sleeps. Exactly one provider request across
// restart rules out replay.
//
// The fixture withholds completion until the client receives a live delta.
// This proves streaming without a timing assumption or a real credential.
func TestHeadlessConversationFixture(t *testing.T) {
	t.Parallel()
	requireLinuxCoreInstall(t)

	var calls atomic.Int32
	deltaSeen := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			t.Error("unexpected provider replay")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var request struct {
			Model         string
			Input         []provider.Item
			Store, Stream bool
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("wrong configured request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "fixture-model" || request.Store || !request.Stream || !strings.Contains(string(mustJSON(t, request.Input)), headlessQAInput) {
			t.Error("client input did not reach the configured stateless model route")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Hello\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-deltaSeen:
		case <-r.Context().Done():
			return
		}
		emitResponse(w, "Hello!", true)
	}))
	t.Cleanup(server.Close)
	env := map[string]string{"LINA_RESPONSES_BASE_URL": server.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}
	headlessConversationRoundTrip(t, func(name string) string { return env[name] }, func() { once.Do(func() { close(deltaSeen) }) })
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one across restart", calls.Load())
	}
}

// TestRealModelOptIn checks that either missing opt-in or missing key skips
// before configuration or model work. The real journey also reports SKIP in
// ordinary credential-free CI; a successful provider probe alone remains
// insufficient evidence of a client-level persisted turn.
//
// Serial: these guards change the test process provider environment.
func TestRealModelOptIn(t *testing.T) {
	for _, tc := range []struct{ name, optIn, key string }{
		{"neither", "", ""}, {"no-opt-in", "", "synthetic-key"}, {"no-key", "1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LINA_QA_REAL_MODEL", tc.optIn)
			t.Setenv("LINA_RESPONSES_API_KEY", tc.key)
			reached := false
			t.Run("guard", func(t *testing.T) {
				requireRealModel(t)
				reached = true
			})
			if reached {
				t.Fatal("real model guard allowed an unconfigured external call")
			}
		})
	}
}

func headlessConversationRoundTrip(t *testing.T, getenv func(string) string, onDelta func()) {
	t.Helper()
	if cfg, err := modelpath.ResponsesFromEnv(getenv); err != nil || cfg == nil {
		t.Fatalf("Core provider configuration: %v", err)
	}
	h := newHarness(t, buildLina(t))
	bundle := makeInstallBundle(t, h.root, h.binary, buildCommand(t, "lina-node"))
	// Turn preparation verifies the installed binary and its manifest.
	installConversationBundle(t, h, bundle, "")
	// Real model latency gets a bounded budget independent of fixture deadlines.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	// Only the explicitly selected route crosses into Core's isolated environment.
	var env []string
	for _, name := range []string{"LINA_RESPONSES_BASE_URL", "LINA_RESPONSES_MODEL", "LINA_RESPONSES_API_KEY"} {
		env = append(env, name+"="+getenv(name))
	}
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, env...)
		core.start(t)
		h.connect(core).Close()
		return core
	}
	core := start()
	client, conversation := conversationClient(t, h)
	inputID, input := "headless-qa-input", headlessQAInput
	requestID, err := client.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &inputID, Text: &input})
	if err != nil {
		t.Fatal(err)
	}
	ack, active, terminal := false, false, false
	deltas := 0
	streamed := make(map[string]bool)
	var completed []partialHistoryRow
	for !ack || !terminal {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatalf("conversation error: %s", frame.Error.Code)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == requestID {
			item, ok := frame.Payload.(protocol.ItemDone)
			if !ok || item.EchoKey == nil || *item.EchoKey != inputID || item.Text == nil || *item.Text != input || frame.EffectState != protocol.EffectApplied {
				t.Fatal("input was not durably acknowledged")
			}
			ack = true
		}
		if delta, ok := frame.Payload.(protocol.OutputDelta); ok && delta.Text != nil && *delta.Text != "" {
			if terminal || delta.Stream == nil {
				t.Fatal("delta is late or has no stream identity")
			}
			deltas++
			streamed[delta.Stream.Key] = true
			if onDelta != nil {
				onDelta()
			}
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil {
			if *item.Speaker == protocol.ConversationSpeakerSystem {
				t.Fatal("turn produced a system outcome instead of a successful reply")
			}
			if *item.Speaker == protocol.ConversationSpeakerAgent {
				if terminal || item.Stream == nil || item.Text == nil || strings.TrimSpace(*item.Text) == "" {
					t.Fatal("invalid completed assistant item")
				}
				completed = append(completed, partialHistoryRow{item.Stream.Key, *item.Text, *item.Speaker})
			}
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil {
			if *state.Active {
				active = true
			} else {
				terminal = true
			}
		}
	}
	if !active || deltas == 0 || len(completed) == 0 {
		t.Fatalf("incomplete client turn: active=%t deltas=%d final_items=%d", active, deltas, len(completed))
	}
	for _, item := range completed {
		delete(streamed, item.Key)
	}
	if len(streamed) != 0 {
		t.Fatal("streamed output was not replaced by completed items")
	}
	live := partialHistory(t, h, client, conversation)
	if len(live) != 1+len(completed) || live[0].Text != input || live[0].Speaker != protocol.ConversationSpeakerPerson || !reflect.DeepEqual(live[1:], completed) {
		t.Fatal("live history differs from the admitted input and completed reply")
	}
	epoch := client.Welcome.Epoch
	client.Close()
	h.stopCore(core)
	core = start()
	reopened, restoredConversation := conversationClient(t, h)
	if reopened.Welcome.Epoch == epoch || restoredConversation != conversation {
		t.Fatal("restart did not preserve conversation identity in a new epoch")
	}
	restored := partialHistory(t, h, reopened, restoredConversation)
	if !reflect.DeepEqual(live, restored) {
		t.Fatal("canon history changed after Core restart")
	}
	reopened.Close()
	h.stopCore(core)
	t.Logf("Headless Core conversation: input_committed=true deltas=%d final_items=%d terminal=true canon_restored=true restored_rows=%d", deltas, len(completed), len(restored))
}
