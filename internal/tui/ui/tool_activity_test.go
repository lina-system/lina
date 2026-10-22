// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

const toolTopic = "conversation:main:fixture"

var toolStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Raw JSON keeps these regressions runnable before the additive generated
// fields exist. The old code compiles, then fails on projection/display.
func toolTurn(t *testing.T, seq int, phase, name string) protocol.Envelope {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"active": true, "phase": phase, "tool_name": name,
		"stream": map[string]any{"topic": toolTopic, "key": "turn", "revision": seq,
			"cursor": map[string]any{"epoch": "1", "seq": seq}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload protocol.TurnState
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{Kind: protocol.KindTurnState, Epoch: "1", SentAt: toolStart.Add(time.Duration(seq-1) * time.Second).Format(time.RFC3339Nano), Payload: payload}
}

func applyToolProjection(t *testing.T, m *shellModel, envelope protocol.Envelope) {
	t.Helper()
	p, err := projectStream(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if p.message == nil {
		t.Fatal("stream event did not consume its cursor")
	}
	m.Update(shellData{Generation: 1, Message: p.message})
}

func toolShell(t *testing.T) *shellModel {
	t.Helper()
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: toolTopic, ServerTime: toolStart, ClientTime: toolStart}})
	return m
}

func TestToolActivityPhasesAreAuthoritative(t *testing.T) {
	m := toolShell(t)
	for i, step := range []struct{ phase, name, word string }{
		{"waiting_model", "", design.Text(design.English, "activity.thinking")},
		{"streaming", "", design.Text(design.English, "activity.replying")},
		{"tool", "Reading files", "Reading files"},
		{"tool", "Searching", "Searching"},
		{"waiting_model", "stale tool", design.Text(design.English, "activity.thinking")},
		{"streaming", "stale tool", design.Text(design.English, "activity.replying")},
		{"stopping", "stale tool", design.Text(design.English, "activity.stopping")},
		{"tool", "stale tool", design.Text(design.English, "activity.stopping")},
		{"waiting_model", "stale tool", design.Text(design.English, "activity.stopping")},
	} {
		applyToolProjection(t, m, toolTurn(t, i+1, step.phase, step.name))
		m.Update(activityTick(toolStart.Add(10 * time.Second)))
		if got := m.activityText(); !strings.Contains(got, step.word) || strings.Contains(got, "stale tool") {
			t.Errorf("phase %s: row=%q want word %q", step.phase, got, step.word)
		}
		if !m.content.Activity.Started.Equal(toolStart) {
			t.Errorf("phase %s reset start to %v", step.phase, m.content.Activity.Started)
		}
		if !strings.Contains(m.activityText(), "10s") {
			t.Errorf("phase %s reset elapsed: %q", step.phase, m.activityText())
		}
	}
}

func TestToolActivityDeltaCannotOverridePhaseOrStopping(t *testing.T) {
	for _, phase := range []string{"waiting_model", "tool", "stopping"} {
		t.Run(phase, func(t *testing.T) {
			m := toolShell(t)
			applyToolProjection(t, m, toolTurn(t, 1, phase, "Reading"))
			before := m.activityText()
			m.Update(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: toolTopic, ID: "reply", Cursor: 2, Revision: 1, Text: "answer"}})
			if got := m.activityText(); got != before {
				t.Errorf("delta overrode %s: before=%q after=%q", phase, before, got)
			}
			m.Update(shellData{Generation: 1, Message: state.Interrupt{}})
			applyToolProjection(t, m, toolTurn(t, 3, "tool", "Searching"))
			if !strings.Contains(m.activityText(), design.Text(design.English, "activity.stopping")) {
				t.Error("tool transition overrode local stopping")
			}
		})
	}
}

func TestToolActivitySanitizesDisplayName(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, toolTurn(t, 1, "tool", "\x1b]52;c;PRIVATE\a\x1b[31mReading\x1b[0m\u202e\nfiles\r\t"))
	got := m.activityText()
	if !strings.Contains(got, "Reading files") || strings.ContainsAny(got, "\x1b\n\r\t\u202e") || strings.Contains(got, "PRIVATE") {
		t.Fatalf("unsafe or missing tool word: %q", got)
	}
	m.size.Width = 40
	applyToolProjection(t, m, toolTurn(t, 2, "tool", strings.Repeat("읽기", 100)))
	for _, row := range strings.Split(m.activityText(), "\n") {
		if width := (render.Unicode{}).Width(row); width > 40 {
			t.Fatalf("tool row width=%d", width)
		}
	}
	if !strings.Contains(m.activityText(), "Esc)") {
		t.Fatal("long tool name displaced the interrupt key")
	}
}

func toolEvent(t *testing.T, seq int, status string, calls ...string) protocol.Envelope {
	t.Helper()
	call := "call"
	if len(calls) > 0 {
		call = calls[0]
	}
	body := fmt.Sprintf(`{"stream":{"topic":%q,"key":%q,"revision":%d,"cursor":{"epoch":"1","seq":%d}},"call_id":%q,"tool_name":"TOOL-EVENT-ONLY","status":%q}`, toolTopic, call, seq, seq, call, status)
	if status == "finished" {
		body = strings.TrimSuffix(body, "}") + `,"outcome":"ok","duration_ms":250}`
	}
	payload, err := streamItemPayload(protocol.StreamItem{Kind: protocol.Kind("tool_activity"), Payload: json.RawMessage(body)})
	if err != nil {
		t.Fatalf("tool event projection: %v", err)
	}
	return protocol.Envelope{Kind: protocol.Kind("tool_activity"), Epoch: "1", SentAt: toolStart.Format(time.RFC3339Nano), Payload: payload}
}

func TestToolActivityConsumesCursorWithoutTranscript(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, toolTurn(t, 1, "tool", "Reading"))
	before := m.content
	for i, status := range []string{"started", "finished"} {
		event := toolEvent(t, i+2, status)
		applyToolProjection(t, m, event)
		if len(m.content.Blocks) != len(before.Blocks) || !reflect.DeepEqual(m.content.Activity, before.Activity) || len(m.content.Worked) != 0 {
			t.Fatalf("tool event entered conversation or changed activity: %+v", m.content)
		}
		accepted := m.content
		applyToolProjection(t, m, event)
		if !reflect.DeepEqual(accepted, m.content) {
			t.Fatal("replayed tool event changed state")
		}
	}
	m.Update(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: toolTopic, ID: "reply", Cursor: 4, Revision: 1, Text: "visible answer"}})
	if m.content.Topics[toolTopic].OutOfSync || m.content.Topics[toolTopic].Cursor != 4 {
		t.Fatal("ignored tool event caused a stream gap")
	}
	if got := m.View().Content; !strings.Contains(got, "visible answer") || !strings.Contains(got, "TOOL-EVENT-ONLY") {
		t.Fatalf("missing reply or separate tool row: %q", got)
	}
}

func TestToolActivitySnapshotRestoresPhaseAndSkipsOptionalEvents(t *testing.T) {
	for _, phase := range []string{"waiting_model", "streaming", "tool", "stopping"} {
		t.Run(phase, func(t *testing.T) {
			m := toolShell(t)
			applyToolProjection(t, m, toolTurn(t, 1, "waiting_model", ""))
			body, _ := json.Marshal(map[string]any{"active": true, "phase": phase, "tool_name": "Reading"})
			snapshot := protocol.Envelope{Kind: protocol.KindSnapshot, Epoch: "1", Payload: protocol.Snapshot{
				Topic: toolTopic, Cursor: protocol.StreamCursor{Epoch: "1", Seq: "5"}, RevocationEpoch: "1",
				Items: []protocol.StreamItem{
					{Key: "reply", Kind: protocol.KindOutputDelta, Revision: "1", Payload: json.RawMessage(`{"text":"partial"}`)},
					{Key: "turn", Kind: protocol.KindTurnState, Revision: "2", Payload: body},
					{Key: "call", Kind: protocol.Kind("tool_activity"), Revision: "1", Payload: json.RawMessage(`{"call_id":"call","tool_name":"TOOL-EVENT-ONLY","status":"started"}`)},
					{Key: "future", Kind: protocol.Kind("future_activity"), Revision: "1", Payload: json.RawMessage(`{"text":"FUTURE-EVENT-ONLY"}`)},
				},
			}}
			applyToolProjection(t, m, snapshot)
			words := map[string]string{"waiting_model": design.Text(design.English, "activity.thinking"), "streaming": design.Text(design.English, "activity.replying"), "tool": "Reading", "stopping": design.Text(design.English, "activity.stopping")}
			if !strings.Contains(m.activityText(), words[phase]) || !m.content.Activity.Started.Equal(toolStart) {
				t.Fatalf("snapshot lost phase or observed start: %q %+v", m.activityText(), m.content.Activity)
			}
			if len(m.content.Blocks) != 2 || strings.Contains(m.View().Content, "EVENT-ONLY") {
				t.Fatal("snapshot projected activity into transcript")
			}
			before := m.content
			applyToolProjection(t, m, snapshot)
			if !reflect.DeepEqual(before, m.content) {
				t.Fatal("snapshot replay changed activity")
			}
			fresh := toolShell(t)
			applyToolProjection(t, fresh, snapshot)
			applyToolProjection(t, fresh, toolTurn(t, 6, phase, "Reading"))
			if !fresh.content.Activity.Started.IsZero() || !strings.Contains(fresh.activityText(), design.Text(design.English, "unknown")) {
				t.Fatal("fresh snapshot or phase transition fabricated a start time")
			}
		})
	}
}

func TestToolActivityRemainingParallelCallAndCompletion(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, toolTurn(t, 1, "waiting_model", ""))
	applyToolProjection(t, m, toolTurn(t, 2, "tool", "Reading"))
	applyToolProjection(t, m, toolEvent(t, 3, "started", "reading"))
	applyToolProjection(t, m, toolTurn(t, 4, "tool", "Searching"))
	applyToolProjection(t, m, toolEvent(t, 5, "started", "searching"))
	applyToolProjection(t, m, toolEvent(t, 6, "finished", "searching"))
	// The authoritative turn names the remaining call. Finish does not choose it.
	if !strings.Contains(m.activityText(), "Searching") {
		t.Fatal("tool finish selected a phase or tool label")
	}
	applyToolProjection(t, m, toolTurn(t, 7, "tool", "Reading"))
	if !strings.Contains(m.activityText(), "Reading") {
		t.Fatal("remaining parallel call's label was not displayed")
	}
	applyToolProjection(t, m, toolEvent(t, 8, "finished", "reading"))
	applyToolProjection(t, m, toolTurn(t, 9, "waiting_model", ""))
	applyToolProjection(t, m, toolTurn(t, 10, "streaming", ""))
	m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: toolTopic, Cursor: 11,
		Block: state.Block{ID: "reply", Kind: protocol.KindItemDone, Speaker: protocol.ConversationSpeakerAgent, Revision: 1, Text: "answer"}}})
	idle := toolTurn(t, 12, "streaming", "")
	payload := idle.Payload.(protocol.TurnState)
	inactive := false
	payload.Active = &inactive
	idle.Payload = payload
	applyToolProjection(t, m, idle)
	if m.content.TurnActive || m.content.Stopping || m.activityInterval() != 0 || m.content.Worked["reply"] != 11*time.Second {
		t.Fatalf("completion lost full-turn duration or retained activity: %+v", m.content)
	}
	before := m.content
	applyToolProjection(t, m, idle)
	if !reflect.DeepEqual(before, m.content) {
		t.Fatal("completion replay changed elapsed metadata")
	}
}

func TestToolActivitySnapshotUsesLatestTurnRevision(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, protocol.Envelope{Kind: protocol.KindSnapshot, Epoch: "1", Payload: protocol.Snapshot{
		Topic: toolTopic, Cursor: protocol.StreamCursor{Epoch: "1", Seq: "4"}, RevocationEpoch: "1",
		Items: []protocol.StreamItem{
			{Key: "turn", Kind: protocol.KindTurnState, Revision: "3", Payload: json.RawMessage(`{"active":true,"phase":"tool","tool_name":"Reading"}`)},
			{Key: "turn", Kind: protocol.KindTurnState, Revision: "2", Payload: json.RawMessage(`{"active":false}`)},
		},
	}})
	if !m.content.TurnActive || !strings.Contains(m.activityText(), "Reading") || len(m.content.Worked) != 0 {
		t.Fatalf("older snapshot item overrode current phase: %+v", m.content)
	}
}

func toolFrame(t *testing.T, envelope protocol.Envelope) state.Frame {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(streamFixture(t, "streams/turn_state"), &wire); err != nil {
		t.Fatal(err)
	}
	wire["kind"], wire["epoch"], wire["payload"] = envelope.Kind, envelope.Epoch, envelope.Payload
	wire["sent_at"] = toolStart.Format(time.RFC3339Nano)
	if envelope.SentAt != "" {
		wire["sent_at"] = envelope.SentAt
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return state.Frame{Data: data}
}

func TestToolActivityDecodedFramesAndFocus(t *testing.T) {
	m := toolShell(t)
	for i, event := range []protocol.Envelope{
		toolTurn(t, 1, "waiting_model", ""),
		toolTurn(t, 2, "tool", "Reading"),
		toolEvent(t, 3, "started"),
		toolEvent(t, 4, "finished"),
		toolTurn(t, 5, "waiting_model", ""),
		toolTurn(t, 6, "streaming", ""),
	} {
		m.Update(shellData{Generation: 1, Message: toolFrame(t, event)})
		if m.status.State != Live || m.content.Malformed != 0 || m.content.Topics[toolTopic].Cursor != uint64(i+1) {
			t.Fatalf("decoded event %d failed: state=%s content=%+v", i, m.status.State.Code(), m.content)
		}
		if i >= 1 && i <= 3 && !strings.Contains(m.activityText(), "Reading") {
			t.Fatalf("decoded tool phase lost label: %q", m.activityText())
		}
	}
	if len(m.content.Blocks) != 1 || !m.content.Activity.Started.Equal(toolStart) {
		t.Fatal("decoded tool events entered transcript or reset timer")
	}
	other := toolTurn(t, 7, "tool", "OTHER-CONVERSATION")
	payload := other.Payload.(protocol.TurnState)
	payload.Stream.Topic = "conversation:other"
	other.Payload = payload
	before := m.content
	m.Update(shellData{Generation: 1, Message: toolFrame(t, other)})
	if !reflect.DeepEqual(before, m.content) {
		t.Fatal("background conversation changed focused activity")
	}
	snapshot := protocol.Envelope{Kind: protocol.KindSnapshot, Epoch: "1", Payload: protocol.Snapshot{
		Topic: toolTopic, Cursor: protocol.StreamCursor{Epoch: "1", Seq: "9"}, RevocationEpoch: "1",
		Items: []protocol.StreamItem{
			{Key: "turn", Kind: protocol.KindTurnState, Revision: "8", Payload: json.RawMessage(`{"active":true,"phase":"tool","tool_name":"Searching","future_field":true}`)},
			{Key: "future", Kind: protocol.Kind("future_activity"), Revision: "1", Payload: json.RawMessage(`{"text":"PRIVATE-FUTURE-EVENT"}`)},
		},
	}}
	m.Update(shellData{Generation: 1, Message: toolFrame(t, snapshot)})
	m.Update(shellData{Generation: 1, Message: toolFrame(t, toolTurn(t, 10, "waiting_model", ""))})
	if m.content.Malformed != 0 || m.content.Topics[toolTopic].Cursor != 10 || m.content.Topics[toolTopic].OutOfSync || len(m.content.Blocks) != 1 {
		t.Fatalf("optional snapshot event caused stream gap or entered transcript: %+v", m.content)
	}
}
