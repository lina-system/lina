// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM2WorkerFollowupRecovery runs the separate m2 scenario through the
// installed Core's owner follow-up protocol, then kills Core before any worker
// notification. Real SQLite, processes and files cross the restart boundary.
func TestM2WorkerFollowupRecovery(t *testing.T) {
	var scenario struct {
		Cases                             []string
		Input, Followup, Artifact, Result string
	}
	raw, err := os.ReadFile("../m2/worker-followup-recovery.json")
	if err != nil || json.Unmarshal(raw, &scenario) != nil {
		t.Fatal(err)
	}
	for _, mode := range scenario.Cases {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			steering := strings.HasPrefix(mode, "steer_")
			acknowledged := mode == "followup_reply" || mode == "steer_snapshot_failure"
			recoverable := mode != "followup_pending"
			var calls atomic.Int32
			server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Input, Tools []json.RawMessage }
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid model request")
					return
				}
				if scriptedCodingManager(t, w, req) {
					return
				}
				if calls.Add(1) == 1 {
					emitPersonaCall(w, "start", "worker_start", map[string]any{"workdir": dir, "task_packet": "Preserve this task's result."})
				} else {
					emitResponse(w, "The worker result is available.", true)
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
			read := func() recovery.Record {
				t.Helper()
				db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer closeDB()
				var raw []byte
				var rec recovery.Record
				if err := db.QueryRowContext(h.ctx, "SELECT record FROM worker_recovery WHERE id='recovery-thread'").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &rec); err != nil {
					t.Fatal(err)
				}
				return rec
			}
			core := start()
			c, conv := conversationClient(t, h)
			admittedTurn(t, h, c, conv, "start-followup", scenario.Input)
			c.Close()
			if steering {
				waitRecovery(t, h, func() bool { return read().State == "running" })
			} else {
				waitRecovery(t, h, func() bool { return read().State == "finished" })
				waitRecovery(t, h, func() bool { return workerRecoveryReactions(t, h) == 1 })
			}
			owner, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "followup-owner", []protocol.Capability{{Name: "running", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			request, err := owner.Control(h.ctx, protocol.FollowUp{RunID: "session:recovery-thread", ConversationID: &conv, Text: scenario.Followup})
			if err != nil {
				t.Fatal(err)
			}
			waitRecovery(t, h, func() bool { _, err := os.Stat(filepath.Join(dir, scenario.Artifact)); return err == nil })
			if acknowledged {
				for {
					frame, err := owner.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					if frame.ReplyTo != nil && *frame.ReplyTo == request {
						if (frame.Error != nil) != steering {
							t.Fatal("unexpected input outcome", frame.Error)
						}
						break
					}
				}
			}
			rec := read()
			wantState, wantTurn := "unknown", ""
			if mode == "followup_reply" {
				wantState, wantTurn = "running", "turn-2"
			} else if steering {
				wantTurn = "turn-1"
			}
			if rec.State != wantState || rec.Thread.TurnID != wantTurn || rec.Reservation == "" || rec.Finish != nil {
				t.Fatalf("follow-up not durable before notification: state=%s turn=%s reservation=%q finish=%+v", rec.State, rec.Thread.TurnID, rec.Reservation, rec.Finish)
			}
			reservation := rec.Reservation
			core.killAndWait()
			owner.Close()
			core = start()
			if recoverable {
				waitRecovery(t, h, func() bool { _, err := os.Stat(filepath.Join(dir, "snapshot-requested")); return err == nil })
			}
			if rec := read(); rec.State != "unknown" || rec.Reservation != reservation || rec.Thread.TurnID != wantTurn {
				t.Fatal("restart lost follow-up intent", rec)
			}
			if recoverable {
				if err := os.WriteFile(filepath.Join(dir, "snapshot-release"), []byte("read now"), 0600); err != nil {
					t.Fatal(err)
				}
				waitRecovery(t, h, func() bool { return read().State == "finished" })
				wantReactions := 2
				if steering {
					wantReactions = 1
				}
				waitRecovery(t, h, func() bool { return workerRecoveryReactions(t, h) == wantReactions })
			}
			h.stopCore(core)
			db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer closeDB()
			var reservations int
			if err := db.QueryRowContext(h.ctx, "SELECT count(*) FROM work_worker_reservations WHERE worker_id=?", reservation).Scan(&reservations); err != nil {
				t.Fatal(err)
			}
			wantReservations := 1
			if recoverable {
				wantReservations = 0
				var delivered int
				if err := db.QueryRowContext(h.ctx, `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion' AND json_extract(body,'$.turn')=? AND json_extract(body,'$.output')='Retained worker result.'`, wantTurn).Scan(&delivered); err != nil || delivered != 1 {
					t.Fatalf("follow-up result delivery=%d err=%v", delivered, err)
				}
			}
			if reservations != wantReservations {
				t.Fatalf("directory reservations=%d want=%d", reservations, wantReservations)
			}
			wantTurns := "2"
			if steering {
				wantTurns = "1"
				if raw, _ := os.ReadFile(filepath.Join(dir, "steer-count")); string(raw) != "1" {
					t.Fatalf("steering replayed: %s", raw)
				}
			}
			if raw, _ := os.ReadFile(filepath.Join(dir, "turn-count")); string(raw) != wantTurns {
				t.Fatalf("restart replayed worker input: turns=%s", raw)
			}
			if raw, err := os.ReadFile(filepath.Join(dir, scenario.Artifact)); err != nil || string(raw) != scenario.Result {
				t.Fatalf("follow-up result=%s err=%v", raw, err)
			}
		})
	}
}
