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

// The #34 harness installs real Core and speaks its authenticated protocol.
// Provider responses are scripted; the record tools and their Git commits are real.
func TestM2TimersInstalledCore(t *testing.T) {
	t.Parallel()
	var scenario struct {
		Reminder     string   `json:"reminder"`
		SourceTitle  string   `json:"source_title"`
		SourceNote   string   `json:"source_note"`
		UpdatedNote  string   `json:"updated_note"`
		JobResult    string   `json:"job_result"`
		SensorResult string   `json:"sensor_result"`
		Tools        []string `json:"tools"`
	}
	raw, err := os.ReadFile("../m2/timers.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls, sensors, jobs atomic.Int32
	// All requests use one serialized conversation lane.
	var source coreexec.RecordReceipt
	var sensor timers.Timer
	saved := make(chan timers.Timer, 1)
	results := make(chan coreexec.RecordReceipt, 4)
	anchor := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Input []json.RawMessage
			Tools []struct{ Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		names := map[string]bool{}
		for _, tool := range req.Tools {
			names[tool.Name] = true
		}
		for _, name := range scenario.Tools {
			if !names[name] {
				t.Errorf("Core omitted %s", name)
			}
		}
		var last struct{ Type, CallID, Output, Role, Content string }
		// Provider wire uses call_id rather than the Go field spelling.
		var wire struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if len(req.Input) == 0 || json.Unmarshal(req.Input[len(req.Input)-1], &wire) != nil {
			t.Error("no current input")
			return
		}
		last.Type, last.CallID, last.Output, last.Role, last.Content = wire.Type, wire.CallID, wire.Output, wire.Role, wire.Content
		output := func(out any) {
			var result struct {
				Code   string
				Output json.RawMessage
			}
			if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" {
				t.Errorf("tool %s: %s %v", last.CallID, last.Output, err)
				return
			}
			if err := json.Unmarshal(result.Output, out); err != nil {
				t.Error(err)
			}
		}
		create := func(id string, s timers.Schedule, a timers.Action) {
			emitPersonaCall(w, id, "create_timer", map[string]any{"schedule": s, "action": a})
		}
		if last.Type == "function_call_output" {
			switch last.CallID {
			case "source":
				output(&source)
				create("sensor", timers.Schedule{Start: anchor, Zone: "UTC", RRule: "FREQ=DAILY;INTERVAL=365"}, timers.Action{Kind: "sensor", Text: "Read the changed source and save the observation.", Record: source.ID})
			case "sensor":
				output(&sensor)
				saved <- sensor
				create("reminder", timers.Schedule{At: anchor}, timers.Action{Kind: "reminder", Text: scenario.Reminder})
			case "reminder":
				var result timers.Timer
				output(&result)
				create("job", timers.Schedule{At: anchor}, timers.Action{Kind: "job", Text: "Save a scheduled job result."})
			case "job":
				var result timers.Timer
				output(&result)
				emitResponse(w, "Timers saved.", true)
			case "job-result":
				var result coreexec.RecordReceipt
				output(&result)
				results <- result
				emitResponse(w, "Scheduled result saved.", true)
			case "sensor-read":
				var result coreexec.RecordReceipt
				output(&result)
				emitPersonaCall(w, "sensor-result", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.SensorResult, Note: result.Markdown})
			case "sensor-result":
				var result coreexec.RecordReceipt
				output(&result)
				results <- result
				emitResponse(w, "Observation saved.", true)
			case "revise":
				output(&source)
				emitResponse(w, "Source updated.", true)
			case "status":
				var status timers.Status
				output(&status)
				if len(status.Fires) != 3 {
					t.Errorf("sensor fires=%+v", status.Fires)
				}
				emitPersonaCall(w, "cancel", "cancel_timer", map[string]string{"id": sensor.ID})
			case "cancel":
				var status timers.Status
				output(&status)
				if status.Enabled {
					t.Error("cancel did not persist")
				}
				emitResponse(w, "Timer cancelled.", true)
			default:
				t.Errorf("unexpected call %s", last.CallID)
				emitResponse(w, "Unexpected tool result.", true)
			}
			return
		}
		_, body, _ := strings.Cut(last.Content, "\n")
		var envelope struct{ Text string }
		if json.Unmarshal([]byte(body), &envelope) != nil {
			envelope.Text = last.Content
		}
		switch envelope.Text {
		case "Set up the timers.":
			emitPersonaCall(w, "source", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.SourceTitle, Note: scenario.SourceNote})
		case "Revise the source.":
			emitPersonaCall(w, "revise", "write_record", coreexec.RecordWrite{ID: source.ID, Revision: source.Revision, Kind: "memo", Title: scenario.SourceTitle, Note: scenario.UpdatedNote})
		case "Read and cancel the sensor.":
			emitPersonaCall(w, "status", "timer_status", map[string]string{"id": sensor.ID})
		default:
			_, payload, _ := strings.Cut(envelope.Text, "\n")
			var action timers.Action
			if err := json.Unmarshal([]byte(payload), &action); err != nil {
				t.Errorf("unexpected input: %s", last.Content)
				emitResponse(w, "Unexpected input.", true)
				return
			}
			switch action.Kind {
			case "job":
				jobs.Add(1)
				emitPersonaCall(w, "job-result", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.JobResult, Note: "Created by the scheduled Core turn."})
			case "sensor":
				sensors.Add(1)
				emitPersonaCall(w, "sensor-read", "read_record", coreexec.RecordRead{ID: action.Record})
			default:
				t.Errorf("unexpected action=%+v", action)
				emitResponse(w, "Unexpected action.", true)
			}
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "setup-timers", "Set up the timers.")
	var timer timers.Timer
	select {
	case timer = <-saved:
	case <-h.ctx.Done():
		t.Fatal("timer admission missing")
	}
	waitTimerFires(t, h, "", 2)
	select {
	case result := <-results:
		if !strings.Contains(result.Markdown, scenario.JobResult) {
			t.Fatal(result)
		}
	case <-h.ctx.Done():
		t.Fatal("job produced no record")
	}
	rows := partialHistory(t, h, c, conversation)
	reminders := 0
	for _, row := range rows {
		if row.Text == scenario.Reminder {
			reminders++
		}
	}
	if reminders != 1 || jobs.Load() != 1 || sensors.Load() != 0 {
		t.Fatalf("reminders=%d jobs=%d sensors=%d", reminders, jobs.Load(), sensors.Load())
	}
	c.Close()
	h.stopCore(core)
	// Move only the scheduler-owned due field while Core is stopped. Each
	// persisted instant is distinct; the authored annual interval stays fixed.
	// This is a clock-advance fixture, never a production clock override.
	for phase := 0; phase < 3; phase++ {
		dueTimer(t, h, timer.ID, anchor.Add(time.Duration(phase)*time.Second))
		before := calls.Load()
		core = start(true)
		waitTimerFires(t, h, timer.ID, phase+1)
		if phase == 1 && calls.Load() != before {
			t.Fatalf("unchanged signature called model: before=%d after=%d", before, calls.Load())
		}
		if phase != 1 {
			select {
			case result := <-results:
				if !strings.Contains(result.Markdown, scenario.SensorResult) {
					t.Fatal(result)
				}
			case <-h.ctx.Done():
				t.Fatal("changed sensor produced no record")
			}
		}
		if phase == 1 {
			c, conversation = conversationClient(t, h)
			admittedTurn(t, h, c, conversation, "revise-source", "Revise the source.")
			c.Close()
		}
		h.stopCore(core)
	}
	before := calls.Load()
	core = start(true)
	c, conversation = conversationClient(t, h)
	if calls.Load() != before || sensors.Load() != 2 || jobs.Load() != 1 {
		t.Fatalf("restart replay: calls=%d/%d sensors=%d jobs=%d", calls.Load(), before, sensors.Load(), jobs.Load())
	}
	rows = partialHistory(t, h, c, conversation)
	reminders = 0
	for _, row := range rows {
		if row.Text == scenario.Reminder {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("restart reminder rows=%d", reminders)
	}
	admittedTurn(t, h, c, conversation, "cancel-sensor", "Read and cancel the sensor.")
	c.Close()
	h.stopCore(core)
}

func dueTimer(t *testing.T, h *harness, id string, at time.Time) {
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
	if err = db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(h.ctx, "UPDATE timer SET next_at=? WHERE id=?", at.UTC().Format("2006-01-02T15:04:05.000000000Z"), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func waitTimerFires(t *testing.T, h *harness, id string, want int) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		if err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM timer_fire WHERE state='handled' AND (?='' OR timer_id=?)", id, id).Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		if count > want {
			t.Fatalf("extra fires: %d > %d", count, want)
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("fires=%d want=%d: %v", count, want, h.ctx.Err())
		case <-ticker.C:
		}
	}
}
