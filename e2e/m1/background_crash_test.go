// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1BackgroundRunSIGKILL(t *testing.T) {
	const goal, artifact = "CRASH-RUN-122", "RECOVERED-RUN-122"
	var foreground, background atomic.Int32
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Instructions string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			if background.Add(1) == 1 {
				started <- struct{}{}
				<-r.Context().Done()
				return
			}
			emitResponse(w, string(mustJSON(t, runs.Artifact{Body: artifact, Done: true})), true)
			return
		}
		switch n := foreground.Add(1); {
		case n <= 6:
			emitPersonaCall(w, fmt.Sprintf("read-%d", n), "skill_index", map[string]any{})
		case n == 7:
			emitPersonaCall(w, "start", "start_run", runs.Brief{Goal: goal, Scope: "read skill index", NotTouch: "writes", Done: "inventory checked", Result: "summary", Allow: []string{"skill_index"}, Checks: []runs.Check{{Name: "inventory", Kind: runs.Claimed}}})
		case n == 8:
			emitResponse(w, "Synthetic foreground completion", true)
		default:
			t.Error("accepted conversation replayed")
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "crash-run-input", goal)
	before, err := strconv.ParseUint(c.Welcome.Epoch, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal("background did not start")
	}
	core.killAndWait()
	if status, ok := core.command.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL: %v", core.err)
	}
	core = start(true)
	defer h.stopCore(core)
	running, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "running", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	after, err := strconv.ParseUint(running.Welcome.Epoch, 10, 64)
	if err != nil || after <= before {
		t.Fatalf("epoch did not advance: %d -> %d %v", before, after, err)
	}
	topic := "running"
	if _, err := running.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := running.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(mustJSON(t, frame)), "ready_for_review") {
			break
		}
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err := db.QueryRowContext(h.ctx, "SELECT record FROM background_runs").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record runs.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Retries != 1 || record.Outcome != runs.Complete || record.Artifact.Body != artifact || record.Material.ID == "" {
		t.Fatalf("recovered run: %+v", record)
	}
	var count int
	if err := db.QueryRowContext(h.ctx, "SELECT count(*) FROM background_runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("run count=%d: %v", count, err)
	}
	if foreground.Load() != 8 || background.Load() != 2 {
		t.Fatalf("replay counts: foreground=%d background=%d", foreground.Load(), background.Load())
	}
}
