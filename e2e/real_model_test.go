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
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

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

// This acceptance journey remains visible in QA output until #55/#108 connect
// conversation input, streaming and canon. Never count this skip as acceptance.
func TestRealModelHeadlessConversation(t *testing.T) {
	requireRealModel(t)
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	client := h.client(core)
	h.inputRefused(client)
	h.stopCore(core)
	t.Skip("client-level streamed and persisted turn awaits conversation wiring (#55, #108); current Core refuses input")
}
