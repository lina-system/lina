// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/moirai"
	workerdb "github.com/thisisjun786/lina/internal/moirai/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The installed Core owns admission, tools, summary publication and restart.
// Only the provider is a fixture. The optional worker uses Core's real socket.
func TestM2ConversationCompactionThroughRunningCore(t *testing.T) {
	var summaries, requests, toolPhase atomic.Int32
	var lastArchive atomic.Int32
	lastArchive.Store(-1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var last struct{ Role, Content, Type, Output string }
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Role == "developer" && strings.Contains(last.Content, "unanswered_requests") {
			summaries.Add(1)
			emitResponse(w, `{"unanswered_requests":"none","topics_and_conclusions":"Archive","completed_actions":"Skills read and archive recorded.","specific_values":"receipt 42","omitted_detail":"Repeated archive material"}`, true)
			return
		}
		requests.Add(1)
		switch toolPhase.Load() {
		case 0:
			toolPhase.Add(1)
			emitPersonaCall(w, "load-skills", "load_tools", map[string]any{"tools": []string{"skill_index"}})
			return
		case 1, 2:
			if last.Type != "function_call_output" || !strings.Contains(last.Output, `"code":"ok"`) {
				t.Errorf("real tool failed: %s", last.Output)
			}
			if toolPhase.Add(1) == 2 {
				emitPersonaCall(w, "skills", "skill_index", map[string]any{})
				return
			}
			// Cross the preparation trigger only after the completed answer. This
			// result stays out of the foreground until a later compaction publishes it.
			size := len(req.Instructions)
			for _, items := range [][]json.RawMessage{req.Input, req.Tools} {
				for _, item := range items {
					size += len(item)
				}
			}
			emitResponse(w, strings.Repeat("Archive detail. ", max(1, (116000-size)/16+1)), true)
			return
		}
		raw, _ := json.Marshal(req.Input)
		if summaries.Load() > 0 {
			if !bytes.Contains(raw, []byte("specific_values")) {
				t.Error("next request lost its published summary")
			}
			if bytes.Contains(raw, []byte("Archive detail. Archive detail.")) {
				t.Error("covered raw answer returned beside the summary")
			}
		}
		previous := lastArchive.Load()
		if previous >= 0 && !bytes.Contains(raw, []byte(fmt.Sprintf("Archive item %d:", previous))) {
			t.Errorf("retained tail lost archive %d", previous)
		}
		emitResponse(w, "Recorded.", true)
	}))
	defer server.Close()
	h, start := compactionCore(t, server.URL)
	stop := start()
	c, conversation := conversationClient(t, h)
	compactionTurn(t, h, c, conversation, "prepare", "Read available skills and prepare the archive.")
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.Read(h.ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(h.ctx, query).Scan(&n) }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("SELECT count(*) FROM sum_node") != 0 {
		t.Fatal("delivery published a summary")
	}
	reader, err := workerdb.Open(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ready, err := reader.Ready(h.ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	prepared := 0
	for _, work := range ready {
		if !strings.HasPrefix(work.Job.ID, "summary:") {
			continue
		}
		if err := (moirai.Worker{Core: jobapi.NewClient(filepath.Join(h.root, "run/jobs.sock"))}).Step(h.ctx, work); err != nil {
			t.Fatal(err)
		}
		prepared++
	}
	if prepared != 1 || summaries.Load() != 1 || count("SELECT count(*) FROM sum_node") != 0 {
		t.Fatalf("preparation: jobs=%d calls=%d", prepared, summaries.Load())
	}
	for i := range 18 {
		text := fmt.Sprintf("Archive item %d: receipt %d. ", i, 42+i) + strings.Repeat("archive material ", 650)
		compactionTurn(t, h, c, conversation, fmt.Sprintf("archive-%d", i), text)
		lastArchive.Store(int32(i))
		if i == 0 && summaries.Load() != 1 {
			t.Fatal("foreground did not reuse the committed preparation")
		}
	}
	nodes := count("SELECT count(*) FROM sum_node WHERE status='ready'")
	if nodes < 2 || count("SELECT count(*) FROM sum_cover WHERE reason=''") < 3 {
		t.Fatal("live conversation did not publish proven summary coverage")
	}
	if count("SELECT count(*) FROM conv_event WHERE speaker_kind='person'") != 19 {
		t.Fatal("compaction deleted or copied person inputs")
	}
	if count("SELECT count(*) FROM turn_calls WHERE json_extract(result,'$.code')='ok'") < 2 {
		t.Fatal("real tool calls lack successful durable receipts")
	}
	if count("SELECT count(*) FROM conv_event WHERE scaffold_kind='compaction_recorded' AND json_extract(body,'$.kind')='request_sizes'") < 20 {
		t.Fatal("request sizes were not recorded")
	}
	before := summaries.Load()
	c.Close()
	stop()
	stop = start()
	defer stop()
	c, conversation = conversationClient(t, h)
	compactionTurn(t, h, c, conversation, "restart", "Continue after restart.")
	if summaries.Load() != before {
		t.Fatal("restart regenerated a valid summary")
	}
	if count("SELECT count(*) FROM conv_event WHERE speaker_kind='person'") != 20 {
		t.Fatal("restart lost or duplicated input")
	}
	t.Logf("published nodes=%d summary calls=%d foreground requests=%d; prepared through Core Aux/Apply and restored after restart", nodes, summaries.Load(), requests.Load())
}

func compactionTurn(t *testing.T, h *harness, c *clientapi.Client, conversation, key, text string) {
	t.Helper()
	id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	ack, done, answer := false, false, false
	for !ack || !done {
		e, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Error != nil {
			t.Fatalf("Core refusal: %s", e.Error.Code)
		}
		if e.ReplyTo != nil && *e.ReplyTo == id {
			ack = true
		}
		if item, ok := e.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent {
			answer = true
		}
		if _, ok := e.Payload.(protocol.Result); ok {
			answer = true
		}
		if state, ok := e.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			done = true
		}
	}
	if !answer {
		t.Fatal("Core ended the turn without its result")
	}
}

func compactionCore(t *testing.T, url string) (*harness, func() func()) {
	t.Helper()
	return installedCore(t, "LINA_RESPONSES_BASE_URL="+url+"/v1", "LINA_RESPONSES_MODEL=fixture", "LINA_RESPONSES_API_KEY=synthetic-key")
}

// installedCore builds and installs the commands once, then returns a start
// function whose provider route comes from the given environment.
func installedCore(t *testing.T, routeEnv ...string) (*harness, func() func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	root, err := os.MkdirTemp("", "lina-compaction-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	binaries := map[string]string{}
	for _, name := range []string{"lina", "lina-node"} {
		binary := filepath.Join(root, name)
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-ldflags=-w", "-o", binary, "./cmd/"+name)
		cmd.Dir = "../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		binaries[name] = binary
	}
	bin := filepath.Join(root, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\ncase \"$2\" in show) printf 'not-found\\n';; daemon-reload|enable) exit 0;; *) exit 97;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin, "HOME=" + root, "XDG_CONFIG_HOME=" + root, "LANG=C"}
	h := &harness{ctx: ctx, root: filepath.Join(root, "state")}
	bundle := makeInstallBundle(t, root, binaries["lina"], binaries["lina-node"])
	setup := exec.CommandContext(ctx, binaries["lina"], "install", "--state-root", h.root, "--unit-dir", filepath.Join(root, "units"), "--bundle", bundle)
	setup.Env = env
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(h.root, "system/install.json"))
	if err != nil {
		t.Fatal(err)
	}
	var installed install.Result
	if err := json.Unmarshal(raw, &installed); err != nil {
		t.Fatal(err)
	}
	h.socket = installed.Socket
	return h, func() func() {
		cmd := exec.CommandContext(ctx, filepath.Join(h.root, "system/bin/lina"), "core", "--state-root", h.root, "--socket", h.socket)
		cmd.Env = append(append([]string{}, env...), routeEnv...)
		var logs bytes.Buffer
		h.logs = &logs
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Core: %v\n%s", err, &logs)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Errorf("Core did not stop: %s", &logs)
			}
		}
		t.Cleanup(stop)
		for {
			conn, err := net.DialTimeout("unix", h.socket, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			select {
			case err := <-done:
				stopped = true
				t.Fatalf("Core startup: %v\n%s", err, &logs)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
		return stop
	}
}
