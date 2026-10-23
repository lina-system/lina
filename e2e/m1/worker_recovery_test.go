// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The peer survives in durable fixture state, just as Codex's persisted thread
// does. A gated snapshot separates crash recovery from starting another turn.
func recoveryWorkerFixture() {
	if os.Args[1] == "--version" {
		fmt.Println("codex-cli 99.0.0")
		return
	}
	dir := os.Getenv("LINA_RECOVERY_DIR")
	mode := os.Getenv("LINA_RECOVERY_CASE")
	turns := 0
	if raw, err := os.ReadFile(filepath.Join(dir, "turn-count")); err == nil {
		turns, _ = strconv.Atoi(string(raw))
	}
	state := "idle"
	scan := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scan.Scan() {
		var f struct {
			ID     json.RawMessage
			Method string
			Params map[string]json.RawMessage
		}
		if json.Unmarshal(scan.Bytes(), &f) != nil {
			os.Exit(81)
		}
		reply := func(v any) {
			if enc.Encode(map[string]any{"id": f.ID, "result": v}) != nil {
				os.Exit(82)
			}
		}
		notify := func(method string, p any) {
			if enc.Encode(map[string]any{"method": method, "params": p}) != nil {
				os.Exit(83)
			}
		}
		thread := func() map[string]any {
			return map[string]any{"id": "recovery-thread", "cwd": dir, "status": map[string]any{"type": state}}
		}
		switch f.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "recovery-fixture"})
		case "initialized":
		case "thread/start":
			if turns != 0 {
				os.Exit(84)
			}
			reply(map[string]any{"thread": thread(), "cwd": dir, "model": "own"})
		case "thread/resume":
			if len(f.Params) != 1 {
				os.Exit(85)
			}
			reply(map[string]any{"thread": thread(), "cwd": dir, "model": "own"})
		case "turn/start":
			turns++
			if turns > 2 {
				os.Exit(86)
			}
			_ = os.WriteFile(filepath.Join(dir, "turn-count"), []byte(strconv.Itoa(turns)), 0600)
			turn := fmt.Sprintf("turn-%d", turns)
			state = "active"
			notify("turn/started", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": turn, "status": "inProgress"}})
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
			if turns == 1 {
				_ = os.WriteFile(filepath.Join(dir, "recovery-result.txt"), []byte("The worker preserved its work."), 0600)
				if mode == "disconnected" {
					return // Lose the completion notification after producing the result.
				}
			} else {
				// The second attempt observes the retained artifact and fails explicitly.
				raw, err := os.ReadFile(filepath.Join(dir, "recovery-result.txt"))
				if err != nil || string(raw) != "The worker preserved its work." {
					os.Exit(87)
				}
				state = "idle"
				notify("turn/completed", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": turn, "status": "failed"}})
			}
		case "thread/read":
			reply(map[string]any{"thread": thread()})
		case "thread/turns/list":
			_ = os.WriteFile(filepath.Join(dir, "snapshot-requested"), []byte("read"), 0600)
			for {
				if _, err := os.Stat(filepath.Join(dir, "snapshot-release")); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			status := "failed"
			if mode == "completed" || mode == "disconnected" {
				status = "completed"
			}
			items := []any{map[string]any{"type": "agentMessage", "id": "message", "text": "Retained worker result."}}
			if mode == "unknown" {
				items = append(items, map[string]any{"type": "commandExecution", "id": "unsettled", "status": "inProgress"})
			}
			reply(map[string]any{"data": []any{map[string]any{"id": fmt.Sprintf("turn-%d", turns), "status": status, "itemsView": "full", "items": items}}})
		default:
			os.Exit(88) // No interrupt, setting change, or command replay allowed.
		}
	}
}

func TestM2WorkerRecovery(t *testing.T) {
	var scenario struct {
		Cases                   []string
		Input, Artifact, Result string
	}
	raw, err := os.ReadFile("../m2/worker-recovery.json")
	if err != nil || json.Unmarshal(raw, &scenario) != nil {
		t.Fatal(err)
	}
	for _, mode := range scenario.Cases {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Input, Tools []json.RawMessage }
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid request")
					return
				}
				if scriptedCodingManager(t, w, req) {
					return
				}
				step := calls.Add(1)
				if step == 1 {
					emitPersonaCall(w, "start", "worker_start", map[string]any{"workdir": dir, "task_packet": "Preserve this task's result."})
				} else if step == 2 {
					emitResponse(w, "The worker is running.", true)
				} else {
					emitResponse(w, "The recovered result is available.", true)
				}
			}))
			defer server.Close()
			h, bootstrap := installedConversationHarness(t, server.URL)
			seedCodingFixture(t, h, bootstrap)
			bin := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
				t.Fatal(err)
			}
			start := func() *process {
				p := h.command("core", "--state-root", h.root, "--socket", h.socket)
				p.command.Env = append(p.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=3", "LINA_RECOVERY_DIR="+dir, "LINA_RECOVERY_CASE="+mode, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
				p.start(t)
				h.connect(p).Close()
				return p
			}
			core := start()
			c, conv := conversationClient(t, h)
			admittedTurn(t, h, c, conv, "start-recovery", scenario.Input)
			waitRecovery(t, h, func() bool {
				raw, err := os.ReadFile(filepath.Join(dir, scenario.Artifact))
				return err == nil && string(raw) == scenario.Result
			})
			completedReactions := func() int {
				db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer closeDB()
				var count int
				if err := db.QueryRowContext(h.ctx, "SELECT count(*) FROM turn_runs t JOIN inbox i ON i.id=t.inbox_id WHERE i.source='worker-finish' AND t.state='completed'").Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			priorReactions := 0
			if mode == "disconnected" {
				priorReactions = 1
				// The provisional notice must commit and finish its reaction before
				// restarting Core; otherwise this would only test a lost process.
				waitRecovery(t, h, func() bool { return completedReactions() == priorReactions })
			}
			c.Close()
			core.killAndWait() // SIGKILL: no graceful worker or Core checkpoint.
			before := calls.Load()
			core = start()
			waitRecovery(t, h, func() bool { _, err := os.Stat(filepath.Join(dir, "snapshot-requested")); return err == nil })
			read := func() recovery.Record {
				db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer closeDB()
				var raw []byte
				var record recovery.Record
				if err = db.QueryRowContext(h.ctx, "SELECT record FROM worker_recovery WHERE id='recovery-thread'").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				return record
			}
			if record := read(); record.State != "unknown" || record.Retries != 0 {
				t.Fatalf("crash did not retain unknown: %+v", record)
			}
			if err = os.WriteFile(filepath.Join(dir, "snapshot-release"), []byte("read now"), 0600); err != nil {
				t.Fatal(err)
			}
			want := "finished"
			count := "1"
			if mode == "retry" {
				want = "decision"
				count = "2"
			}
			if mode == "unknown" {
				want = "unknown"
			}
			lastState := ""
			waitRecovery(t, h, func() bool {
				rec := read()
				if rec.State != lastState {
					t.Logf("recovery state=%s retries=%d turn=%s", rec.State, rec.Retries, rec.Thread.TurnID)
					lastState = rec.State
				}
				return rec.State == want && rec.ObservedAt > 0
			})
			// A protocol read is the barrier after the reconciliation read, including
			// the unknown case. Repeated reads cannot start or interrupt worker turns.
			client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "recovery-reader", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "retry" {
				waitRecovery(t, h, func() bool {
					if _, err = client.Waiting(h.ctx, protocol.WaitingGet{}); err != nil {
						t.Fatal(err)
					}
					frame, err := client.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					list, ok := frame.Payload.(protocol.Waiting)
					return ok && list.Rows != nil && len(*list.Rows) == 1 && (*list.Rows)[0].Ref.Source == "worker"
				})
			}
			client.Close()
			reactions := 0
			if mode == "completed" || mode == "disconnected" {
				reactions = 1
			}
			waitRecovery(t, h, func() bool { return completedReactions() == priorReactions+reactions })
			if mode == "disconnected" {
				db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				var body []byte
				err = db.QueryRowContext(h.ctx, "SELECT body FROM inbox WHERE source='worker-finish' ORDER BY id DESC LIMIT 1").Scan(&body)
				closeDB()
				if err != nil {
					t.Fatal(err)
				}
				var outcome struct {
					Status worker.FinishStatus
					Output string
				}
				if err := json.Unmarshal(body, &outcome); err != nil || outcome.Status != worker.Done || outcome.Output != "Retained worker result." {
					t.Fatalf("recovered result did not reach inbox: %s %v", body, err)
				}
			}
			h.stopCore(core)
			if record := read(); record.State != want {
				t.Fatalf("recovery state=%+v", record)
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "turn-count")); err != nil || string(raw) != count {
				t.Fatalf("turn count=%s want=%s: %v", raw, count, err)
			}
			if calls.Load() != before+int32(reactions) {
				t.Fatalf("result reactions=%d want=%d", calls.Load()-before, reactions)
			}
			if raw, err := os.ReadFile(filepath.Join(dir, scenario.Artifact)); err != nil || string(raw) != scenario.Result {
				t.Fatalf("artifact=%s: %v", raw, err)
			}
			// A second Core restart cannot reset the recovery allowance.
			previousObserved := read().ObservedAt
			core = start()
			if mode == "unknown" {
				waitRecovery(t, h, func() bool { return read().ObservedAt > previousObserved })
			}
			if mode == "retry" {
				restored, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "restored-recovery", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
				if err != nil {
					t.Fatal(err)
				}
				waitRecovery(t, h, func() bool {
					if _, err := restored.Waiting(h.ctx, protocol.WaitingGet{}); err != nil {
						t.Fatal(err)
					}
					frame, err := restored.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					list, ok := frame.Payload.(protocol.Waiting)
					return ok && list.Rows != nil && len(*list.Rows) == 1
				})
				restored.Close()
			}
			h.stopCore(core)
			if raw, _ := os.ReadFile(filepath.Join(dir, "turn-count")); string(raw) != count {
				t.Fatal("restart dispatched a third turn")
			}
			if calls.Load() != before+int32(reactions) {
				t.Fatal("restart replayed result delivery")
			}
		})
	}
}
func waitRecovery(t *testing.T, h *harness, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery observation deadline", ctx.Err())
		case <-tick.C:
		}
	}
}
