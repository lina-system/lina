// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"bytes"
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
	steered := false
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
			if (mode == "followup_reply" || mode == "followup_pending") && turns == 2 {
				_ = os.WriteFile(filepath.Join(dir, "followup-result.txt"), []byte("Follow-up work survived."), 0600)
				if mode == "followup_reply" {
					reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
				}
				continue // No started or completed notification for the follow-up.
			}
			notify("turn/started", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": turn, "status": "inProgress"}})
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
			if turns == 1 {
				_ = os.WriteFile(filepath.Join(dir, "recovery-result.txt"), []byte("The worker preserved its work."), 0600)
				if mode == "followup_reply" || mode == "followup_pending" {
					state = "idle"
					notify("turn/completed", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": turn, "status": "completed"}})
				}
				if mode == "disconnect_restart" {
					return // Persisted work survives loss of both worker and Core.
				}
				if mode == "disconnect" {
					go func() {
						for {
							if _, err := os.Stat(filepath.Join(dir, "disconnect-release")); err == nil {
								os.Exit(0) // Completed work, but no completion notification.
							}
							time.Sleep(time.Millisecond)
						}
					}()
				}
			} else if mode == "delayed" {
				const output = "The follow-up result survived."
				if err := os.WriteFile(filepath.Join(dir, "follow-up.txt"), []byte(output), 0600); err != nil {
					os.Exit(89)
				}
				// The old turn was reconciled after restart. Its first live
				// completion arrives between the new turn's output and finish.
				notify("item/completed", map[string]any{"threadId": "recovery-thread", "turnId": turn, "item": map[string]any{"type": "agentMessage", "id": "follow-up", "text": output}})
				notify("turn/completed", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": "turn-1", "status": "completed"}})
				state = "idle"
				notify("turn/completed", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": turn, "status": "completed"}})
			} else {
				// The second attempt observes the retained artifact and fails explicitly.
				raw, err := os.ReadFile(filepath.Join(dir, "recovery-result.txt"))
				if err != nil || string(raw) != "The worker preserved its work." {
					os.Exit(87)
				}
				state = "idle"
				notify("turn/completed", map[string]any{"threadId": "recovery-thread", "turn": map[string]any{"id": turn, "status": "failed"}})
			}
		case "turn/steer":
			var expected string
			_ = json.Unmarshal(f.Params["expectedTurnId"], &expected)
			if (mode != "steer_snapshot_failure" && mode != "steer_pending") || turns != 1 || expected != "turn-1" || steered {
				os.Exit(90)
			}
			steered = true
			// Persist real work, but emit no event that could repair Core's binding.
			_ = os.WriteFile(filepath.Join(dir, "followup-result.txt"), []byte("Follow-up work survived."), 0600)
			file, err := os.OpenFile(filepath.Join(dir, "steer-count"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(91)
			}
			_, _ = file.WriteString("1")
			_ = file.Close()
			if mode != "steer_pending" {
				reply(map[string]any{"turnId": expected})
			}
		case "thread/read":
			if steered && mode == "steer_snapshot_failure" {
				_ = enc.Encode(map[string]any{"id": f.ID, "error": map[string]any{"code": -32000, "message": "snapshot temporarily unavailable"}})
			} else {
				reply(map[string]any{"thread": thread()})
			}
		case "thread/turns/list":
			_ = os.WriteFile(filepath.Join(dir, "snapshot-requested"), []byte("read"), 0600)
			for {
				if _, err := os.Stat(filepath.Join(dir, "snapshot-release")); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			status := "failed"
			if mode == "completed" || mode == "disconnect" || mode == "disconnect_restart" || mode == "delayed" || mode == "followup_reply" || mode == "steer_snapshot_failure" || mode == "steer_pending" {
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

// TestM2WorkerRecovery runs m2/worker-recovery.json, killing installed Core
// during a synthetic Codex task and gating the retained thread snapshot. It
// proves unknown state before reconciliation, settlement without a new turn,
// one continuation, a second-failure decision, retained files and no third turn
// across restart. Its disconnect case confirms successful output after a
// transport-loss notice without another worker turn or duplicate response.
// disconnect_restart also kills Core after the provisional reaction commits.
// No authenticated worker account is used.
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
			server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Input, Tools []json.RawMessage }
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid request")
					return
				}
				if scriptedCodingManager(t, w, req) {
					return
				}
				step := calls.Add(1)
				if (mode == "disconnect" || mode == "disconnect_restart") && step == 4 {
					context, err := json.Marshal(req.Input)
					if err != nil || !bytes.Contains(context, []byte("Retained worker result.")) {
						t.Error("confirmed worker output missing from result response context")
					}
				}
				if step == 1 {
					emitPersonaCall(w, "start", "worker_start", map[string]any{"workdir": dir, "task_packet": "Preserve this task's result."})
				} else if step == 2 {
					emitResponse(w, "The worker is running.", true)
				} else {
					emitResponse(w, "The recovered result is available.", true)
				}
			})))
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
			c.Close()
			if mode == "disconnect" {
				if err := os.WriteFile(filepath.Join(dir, "disconnect-release"), []byte("close transport"), 0600); err != nil {
					t.Fatal(err)
				}
				// Admit and react to the transport failure before terminal readback.
				waitRecovery(t, h, func() bool { return workerRecoveryReactions(t, h) == 1 })
			} else {
				if mode == "disconnect_restart" {
					waitRecovery(t, h, func() bool { return workerRecoveryReactions(t, h) == 1 })
				}
				core.killAndWait() // SIGKILL: no graceful worker or Core checkpoint.
			}
			before := calls.Load()
			if mode != "disconnect" {
				core = start()
			}
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
			if mode == "disconnect" || mode == "disconnect_restart" {
				db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				var successes int
				err = db.QueryRowContext(h.ctx, `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion' AND json_extract(body,'$.status')='done' AND json_extract(body,'$.output')='Retained worker result.'`).Scan(&successes)
				closeDB()
				if err != nil || successes != 1 {
					t.Fatalf("recovery finished without delivering confirmed output: successes=%d err=%v", successes, err)
				}
			}
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
			if mode == "completed" || mode == "disconnect" || mode == "disconnect_restart" {
				reactions = 1
			}
			priorReactions := 0
			if mode == "disconnect" || mode == "disconnect_restart" {
				priorReactions = 1
			}
			waitRecovery(t, h, func() bool {
				return workerRecoveryReactions(t, h) == priorReactions+reactions
			})
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
func workerRecoveryReactions(t *testing.T, h *harness) int {
	t.Helper()
	db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	var completed int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM turn_runs t JOIN inbox i ON i.id=t.inbox_id WHERE i.source='worker-finish' AND t.state='completed'").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	return completed
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
