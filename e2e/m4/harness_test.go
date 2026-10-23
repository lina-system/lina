// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/e2e/fixture"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type coreHarness struct {
	t                 *testing.T
	ctx               context.Context
	root, socket, url string
	cancel            context.CancelFunc
	done              chan error
}

func newCore(t *testing.T, url string) *coreHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	root, err := os.MkdirTemp("", "lina-m4-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"m4-test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root)
	h := &coreHarness{t: t, ctx: ctx, root: root, socket: filepath.Join(root, "core.sock"), url: url}
	t.Cleanup(h.stop)
	h.start()
	return h
}
func (h *coreHarness) start() {
	h.t.Helper()
	route, err := modelpath.ResponsesFromEnv(func(k string) string {
		switch k {
		case "LINA_RESPONSES_BASE_URL":
			return h.url + "/v1"
		case "LINA_RESPONSES_MODEL":
			return "fixture"
		case "LINA_RESPONSES_API_KEY":
			return "synthetic"
		}
		return ""
	})
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.cancel = cancel
	h.done = make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		h.done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route, AfterStep: func(s boot.Step, _ boot.Snapshot) error {
			if s == boot.Ready {
				close(ready)
			}
			return nil
		}})
	}()
	select {
	case <-ready:
	case err := <-h.done:
		h.cancel = nil
		h.t.Fatalf("start: %v", err)
	case <-h.ctx.Done():
		h.t.Fatal("Core not ready")
	}
}
func (h *coreHarness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("Core shutdown timed out")
	}
}
func (h *coreHarness) read(fn func(*canon.Tx) error) {
	h.t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err = db.Read(h.ctx, fn); err != nil {
		h.t.Fatal(err)
	}
}
func (h *coreHarness) agent(preset string) string {
	var id string
	h.read(func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT id FROM agents WHERE preset=? AND trashed_at=0`, preset).Scan(&id)
	})
	return id
}
func (h *coreHarness) turn(agent, key, text string) {
	h.t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "m4", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		h.t.Fatal(err)
	}
	defer c.Close()
	conversation := "main:" + agent
	topic := "conversation:" + conversation
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		h.t.Fatal(err)
	}
	if _, err = c.Receive(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
	if err != nil {
		h.t.Fatal(err)
	}
	ack, done := false, false
	for !ack || !done {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			h.t.Fatal(err)
		}
		if frame.Error != nil {
			h.t.Fatalf("input: %+v", frame.Error)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == id {
			ack = true
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			done = true
		}
	}
}
func (h *coreHarness) await(query string, want int, args ...any) {
	h.t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		h.read(func(tx *canon.Tx) error { return tx.QueryRowContext(h.ctx, query, args...).Scan(&n) })
		if n == want {
			return
		}
		select {
		case <-h.ctx.Done():
			h.t.Fatalf("condition timed out: %s got=%d want=%d", query, n, want)
		case <-ticker.C:
		}
	}
}
func (h *coreHarness) due(delivery string) {
	h.t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		h.t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		h.t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		h.t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err = db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(h.ctx, `UPDATE exch_origins SET next_check=? WHERE delivery_id=?`, time.Now().Add(-time.Hour).UnixNano(), delivery)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}

type modelRequest struct {
	Instructions string
	Input, Tools []json.RawMessage
}

func decodeRequest(t *testing.T, r *http.Request) modelRequest {
	t.Helper()
	var q modelRequest
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		t.Error(err)
	}
	return q
}
func toolCall(w http.ResponseWriter, id, name string, args any) {
	raw, _ := json.Marshal(args)
	item, _ := json.Marshal(map[string]any{"type": "function_call", "id": "item-" + id, "call_id": id, "name": name, "arguments": string(raw)})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}
func reply(w http.ResponseWriter, text string) {
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

type lastItem struct{ Type, Role, CallID, Output, Content string }

func last(q modelRequest) lastItem {
	var v struct {
		Type, Role      string
		CallID          string `json:"call_id"`
		Output, Content string
	}
	if len(q.Input) > 0 {
		_ = json.Unmarshal(fixture.LastInput(q.Input), &v)
	}
	return lastItem{v.Type, v.Role, v.CallID, v.Output, v.Content}
}

// This in-process Core uses the test executable. Pin its actual bytes through
// the ordinary installed-build gate; only the packaging metadata is synthetic.
func writeManifest(t *testing.T, root string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	policy, err := responses.BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{Version: "0.0.1-alpha.dev", Commit: strings.Repeat("a", 40), Date: "2026-10-23", Target: runtime.GOOS + "/" + runtime.GOARCH, GoToolchain: runtime.Version(), KitVersion: 1, SupportedCombinations: []manifest.Combination{}, Providers: map[string]responses.Config{"chatgpt": policy}, Binaries: []manifest.Binary{{Name: "lina", Size: size, SHA256: digest}, {Name: "lina-node", Size: 1, SHA256: strings.Repeat("b", 64)}}, SQLite: manifest.SQLite{Module: "github.com/ncruces/go-sqlite3", Version: "v0.30.5", ModuleSum: "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=", SQLiteVersion: "3.51.2", WasmSHA256: strings.Repeat("d", 64), Execution: "wazero"}}
	if err = m.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "system/bin/manifest.json")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
