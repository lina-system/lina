// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/timers"
)

func TestM2TimerRecoveryInstalledCore(t *testing.T) {
	t.Parallel()
	var scenario struct {
		SourceTitle string `json:"source_title"`
		SourceNote  string `json:"source_note"`
		ResultTitle string `json:"result_title"`
		Reminder    string `json:"reminder"`
		InvalidRule string `json:"invalid_rule"`
	}
	data, err := os.ReadFile("../m2/timer-recovery.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var source coreexec.RecordReceipt
	saved := make(chan timers.Timer, 1)
	result := make(chan coreexec.RecordReceipt, 1)
	var reactions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Input) == 0 {
			t.Error("missing provider input", err)
			return
		}
		var last struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(req.Input[len(req.Input)-1], &last); err != nil {
			t.Error(err)
			return
		}
		output := func(out any) {
			var value struct {
				Code   string
				Output json.RawMessage
			}
			if err := json.Unmarshal([]byte(last.Output), &value); err != nil || value.Code != "ok" {
				t.Errorf("tool %s: %s (%v)", last.CallID, last.Output, err)
				return
			}
			if err := json.Unmarshal(value.Output, out); err != nil {
				t.Error(err)
			}
		}
		if last.Type == "function_call_output" {
			switch last.CallID {
			case "invalid":
				var value struct {
					Code   string
					Output struct{ Code string }
				}
				if err := json.Unmarshal([]byte(last.Output), &value); err != nil || value.Code != "invalid_arguments" || value.Output.Code != "invalid_timer" {
					t.Errorf("unsafe recurrence admitted: %s (%v)", last.Output, err)
				}
				emitPersonaCall(w, "reminder", "create_timer", map[string]any{"schedule": timers.Schedule{At: time.Now().Add(-time.Hour)}, "action": timers.Action{Kind: "reminder", Text: scenario.Reminder}})
			case "reminder":
				var tm timers.Timer
				output(&tm)
				emitPersonaCall(w, "source", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.SourceTitle, Note: scenario.SourceNote})
			case "source":
				output(&source)
				// Keep the tool-created sensor in the future while setting up the failure fixture.
				emitPersonaCall(w, "sensor", "create_timer", map[string]any{"schedule": timers.Schedule{Start: time.Now().Add(24 * time.Hour).UTC().Truncate(time.Minute), Zone: "UTC", RRule: "FREQ=MINUTELY"}, "action": timers.Action{Kind: "sensor", Text: "Save an observation of the source.", Record: source.ID}})
			case "sensor":
				var tm timers.Timer
				output(&tm)
				saved <- tm
				emitResponse(w, "Timers saved.", true)
			case "read":
				var record coreexec.RecordReceipt
				output(&record)
				emitPersonaCall(w, "result", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.ResultTitle, Note: record.Markdown})
			case "result":
				var record coreexec.RecordReceipt
				output(&record)
				result <- record
				emitResponse(w, "Observation saved.", true)
			default:
				t.Errorf("unexpected tool: %s", last.CallID)
			}
			return
		}
		if strings.Contains(last.Content, "Check timer recovery.") {
			emitPersonaCall(w, "invalid", "create_timer", map[string]any{"schedule": timers.Schedule{Start: time.Now().Add(24 * time.Hour).UTC().Truncate(time.Minute), Zone: "UTC", RRule: scenario.InvalidRule}, "action": timers.Action{Kind: "reminder", Text: "Unsupported interval"}})
		} else {
			reactions.Add(1)
			emitPersonaCall(w, "read", "read_record", coreexec.RecordRead{ID: source.ID})
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "timer-recovery", "Check timer recovery.")
	var tm timers.Timer
	select {
	case tm = <-saved:
	case <-h.ctx.Done():
		t.Fatal("missing sensor")
	}
	waitTimerFires(t, h, "", 1)
	rows := partialHistory(t, h, c, conversation)
	reminders := 0
	for _, row := range rows {
		if row.Text == scenario.Reminder {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("unrelated reminder count=%d", reminders)
	}
	c.Close()
	h.stopCore(core)
	// A real minute recurrence needs up to one minute after source restoration.
	// Extend only this scenario's deadline; never override the production clock.
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	h.ctx = ctx
	// Seed an already due minute recurrence with the admitted binding/action.
	// The future tool-created schedule stays immutable.
	tm = seedRecoveryTimer(t, h, tm)
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var recordPath string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT path FROM records_index WHERE id=?", source.ID).Scan(&recordPath)
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.root, "versions", "records", recordPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	core = start(true)
	waitRecoveryState(t, h, db, tm.ID, "unknown")
	if reactions.Load() != 0 {
		t.Fatal("failed source read called the model")
	}
	// Restoring the exact saved source clears the failure while this Core stays up.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	waitRecoveryState(t, h, db, tm.ID, "handled")
	select {
	case receipt := <-result:
		if !strings.Contains(receipt.Markdown, scenario.ResultTitle) || !strings.Contains(receipt.Markdown, scenario.SourceNote) {
			t.Fatal(receipt)
		}
	case <-h.ctx.Done():
		t.Fatal("recovered sensor produced no saved result")
	}
	if reactions.Load() != 1 {
		t.Fatalf("sensor reactions=%d", reactions.Load())
	}
	h.stopCore(core)
}

func waitRecoveryState(t *testing.T, h *harness, db *canon.Store, id, state string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count, attempts int
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT count(*),coalesce(max(attempts),0) FROM timer_fire WHERE timer_id=? AND state=?", id, state).Scan(&count, &attempts)
		}); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			if state == "unknown" && attempts != 2 {
				t.Fatalf("failure attempts=%d", attempts)
			}
			return
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("timer did not reach %s: %v", state, h.ctx.Err())
		case <-ticker.C:
		}
	}
}

func seedRecoveryTimer(t *testing.T, h *harness, tm timers.Timer) timers.Timer {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	tm.ID += "-recovery-fixture"
	tm.Schedule = timers.Schedule{Cron: "* * * * *", Zone: "UTC"}
	if _, err := timers.New(db).Create(h.ctx, tm, time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	return tm
}
