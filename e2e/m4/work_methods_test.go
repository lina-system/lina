// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type methodWorker struct {
	root string
	ran  chan string
}

func (w *methodWorker) Authorize(_ context.Context, a work.Action) error {
	if a.Kind != work.Assign {
		return work.ErrNeedsPerson
	}
	return nil
}
func (w *methodWorker) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{Status: work.OutcomeUnknown}, nil
}
func (w *methodWorker) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	// A local headless worker creates an artifact. Later PR actions deliberately
	// remain unapproved: this fixture makes no GitHub write or completion claim.
	path := filepath.Join(w.root, a.TaskID+".txt")
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf 'worker result\n' > "$1"`, "worker", path)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return work.Observation{}, fmt.Errorf("worker: %w: %s", err, out)
	}
	w.ran <- path
	return work.Observation{Status: work.OutcomeSucceeded, Head: "fixture-head", EffectsSettled: true}, nil
}

func block(text, name string) string {
	_, tail, ok := strings.Cut(text, "<"+name+">\n")
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(tail, "\n</"+name+">")
	return value
}
func call(w http.ResponseWriter, id, name string, args any) {
	raw, _ := json.Marshal(args)
	item, _ := json.Marshal(map[string]any{"type": "function_call", "id": "item-" + id, "call_id": id, "name": name, "arguments": string(raw)})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}
func answer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Work recorded.\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

func TestWorkMethodsThroughRunningCore(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-m4-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	rounds := map[string]int{}
	catalogs := map[string][]string{}
	first := map[string][]string{}
	var lastState []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			answer(w)
			return
		}
		var method struct{ Preset string }
		if err := json.Unmarshal([]byte(block(req.Instructions, "work_method")), &method); err != nil {
			t.Errorf("missing pinned work method: %v", err)
			answer(w)
			return
		}
		key := method.Preset
		n := rounds[key]
		rounds[key]++
		var names, available []string
		for _, raw := range req.Tools {
			var d struct {
				Name       string
				Parameters struct {
					Properties struct {
						Tools struct{ Items struct{ Enum []string } }
					}
				}
			}
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Error(err)
			}
			names = append(names, d.Name)
			if d.Name == "load_tools" {
				available = d.Parameters.Properties.Tools.Items.Enum
			}
		}
		if n == 0 {
			first[key] = names
			catalogs[key] = available
			if !slices.Contains(names, "load_tools") {
				t.Error("on-demand loader missing")
				answer(w)
				return
			}
			if key == "dev" && (!slices.Contains(names, "dispatch_code_goal") || slices.Contains(names, "create_timer")) {
				t.Errorf("dev first groups: %v", names)
			}
			if key == "lina" && (!slices.Contains(names, "create_timer") || slices.Contains(names, "dispatch_code_goal")) {
				t.Errorf("lina first groups: %v", names)
			}
			call(w, "load", "load_tools", map[string]any{"tools": available})
			return
		}
		// Every supplied tool result must be a real successful Core result.
		var last struct {
			Code   string
			Output json.RawMessage
		}
		for _, raw := range req.Input {
			var item struct{ Type, Output string }
			_ = json.Unmarshal(raw, &item)
			if item.Type == "function_call_output" {
				_ = json.Unmarshal([]byte(item.Output), &last)
			}
		}
		if last.Code != "ok" {
			t.Errorf("%s round %d failed: %+v", key, n, last)
			answer(w)
			return
		}
		switch n {
		case 1:
			for _, name := range catalogs[key] {
				if !slices.Contains(names, name) {
					t.Errorf("%s cannot load %s", key, name)
				}
			}
			if !reflect.DeepEqual(names[:len(first[key])], first[key]) {
				t.Error("loading changed declared prefix")
			}
			call(w, "calculate", "calculate", map[string]string{"expression": "6*7"})
		case 2:
			var result struct {
				Exact string `json:"exact_value"`
			}
			_ = json.Unmarshal(last.Output, &result)
			if result.Exact != "42" {
				t.Errorf("calculation result: %s", last.Output)
			}
			args := map[string]any{"outcome": "Create a fixture result", "done": "Result is inspected", "scope": map[string]any{"repositories": []string{"fixture-repo"}, "includes": "fixture output", "excludes": "publishing"}, "no_service_reason": "Local fixture", "criteria": []any{map[string]any{"id": "result", "statement": "Output exists", "check": map[string]any{"kind": "check", "method": "read", "target": "fixture-repo"}}}, "tasks": []any{map[string]any{"id": key, "done": "Output exists", "repository": "fixture-repo", "base": "dev", "area": key, "worker": "fixture", "resources": map[string]any{"cpu": 1, "memory_mb": 1, "disk_mb": 1}}}}
			call(w, "goal", "dispatch_code_goal", args)
		case 3:
			answer(w)
		default:
			t.Errorf("unexpected request %s:%d", key, n)
			answer(w)
		}
	}))
	defer server.Close()
	route, err := modelpath.ResponsesFromEnv(func(k string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": server.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := &methodWorker{root: root, ran: make(chan string, 2)}
	var engine *work.Engine
	ready := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	done := make(chan error, 1)
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "client.sock"), Conversation: route, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.Ready {
			close(ready)
		}
		return nil
	}, Work: &boot.WorkConfig{Changed: func(_ context.Context, s work.State) error {
		raw, _ := json.Marshal(s)
		mu.Lock()
		lastState = raw
		mu.Unlock()
		return nil
	}, Driver: func(e *work.Engine) (work.Driver, error) { engine = e; return worker, nil }, MainHost: func(context.Context) (work.Node, error) {
		return work.Node{ID: "fixture", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 100, MemoryLimitMB: 100, AvailableDiskMB: 100, DiskLimitMB: 100, Workers: map[string]bool{"fixture": true}}, nil
	}}}
	go func() { done <- boot.Run(ctx, cfg) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Core shutdown: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	reader, err := canon.OpenReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	ids := map[string]string{}
	if err = reader.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT preset,agent_id FROM agent_starters WHERE preset IN ('dev','lina')")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, id string
			if err = rows.Scan(&key, &id); err != nil {
				return err
			}
			ids[key] = id
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lina", "dev"} {
		c, err := clientapi.DialWithCapabilities(ctx, cfg.Socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		conversation := "main:" + ids[key]
		topic := "conversation:" + conversation
		if _, err = c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		if _, err = c.Receive(ctx); err != nil {
			t.Fatal(err)
		}
		text := "Create the local fixture result."
		message, err := c.Input(ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
		if err != nil {
			t.Fatal(err)
		}
		ack, finished := false, false
		for !ack || !finished {
			e, err := c.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.Error != nil {
				t.Fatalf("Core refused: %+v", e.Error)
			}
			if e.ReplyTo != nil && *e.ReplyTo == message {
				ack = true
			}
			if state, ok := e.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
				finished = true
			}
		}
		c.Close()
	}
	for range 2 {
		select {
		case path := <-worker.ran:
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != "worker result\n" {
				t.Fatalf("worker artifact: %s %v", raw, err)
			}
		case <-ctx.Done():
			diagnostic, stop := context.WithTimeout(context.Background(), time.Second)
			state, err := engine.Snapshot(diagnostic)
			stop()
			raw, _ := json.Marshal(state)
			mu.Lock()
			last := string(lastState)
			requests := fmt.Sprint(rounds)
			mu.Unlock()
			t.Fatalf("worker did not execute: %v; snapshot=%s (%v); last=%s; rounds=%s", ctx.Err(), raw, err, last, requests)
		}
	}
	snapshot, err := engine.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Goals) != 2 {
		t.Fatalf("goals=%+v", snapshot.Goals)
	}
	for _, g := range snapshot.Goals {
		if g.Agent != ids["dev"] || len(g.Tasks) != 1 {
			t.Errorf("non-dispatch agent created a worker task: %+v", g)
		}
	}
	exchanges, err := exchange.New(reader).Log(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exchanges) != 1 || exchanges[0].From != ids["lina"] || exchanges[0].To != ids["dev"] || exchanges[0].State != exchange.Delivered {
		t.Fatalf("code exchange: %+v", exchanges)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(catalogs["lina"], catalogs["dev"]) || len(catalogs["dev"]) == 0 {
		t.Error("tool reachability differs by agent")
	}
	if rounds["lina"] != 4 || rounds["dev"] != 4 {
		t.Errorf("rounds=%v", rounds)
	}
}
