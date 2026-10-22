// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTurnPhaseAndToolActivity(t *testing.T) {
	for _, phase := range []string{"waiting_model", "streaming", "tool", "stopping"} {
		t.Run(phase, func(t *testing.T) {
			roundTrip(t, clientMessage(t, "turn_state", map[string]any{
				"active": true, "agent_id": "agent-example", "phase": phase, "tool_name": "Reading file",
			}))
		})
	}
	for _, name := range []string{"tool_started", "tool_finished_ok", "tool_finished_failed", "activity_snapshot"} {
		t.Run(name, func(t *testing.T) {
			data := fixture(t, "conversation/"+name)
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil || !reflect.DeepEqual(jsonValue(t, encoded), jsonValue(t, data)) {
				t.Fatalf("activity round trip: %s, %v", encoded, err)
			}
			var client ClientDecoder
			if got, err := client.Decode(data); err != nil || got == nil {
				t.Fatalf("client rejected activity: %v", err)
			}
		})
	}
}

func TestActivityRejectsInvalidFields(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"missing call id":   func(p map[string]any) { delete(p, "call_id") },
		"missing tool name": func(p map[string]any) { delete(p, "tool_name") },
		"missing status":    func(p map[string]any) { delete(p, "status") },
		"numeric call id":   func(p map[string]any) { p["call_id"] = 1 },
		"null tool name":    func(p map[string]any) { p["tool_name"] = nil },
		"unknown status":    func(p map[string]any) { p["status"] = "running" },
		"missing outcome":   func(p map[string]any) { delete(p, "outcome") },
		"missing duration":  func(p map[string]any) { delete(p, "duration_ms") },
		"unknown outcome":   func(p map[string]any) { p["outcome"] = "cancelled" },
		"negative duration": func(p map[string]any) { p["duration_ms"] = -0.1 },
		"string duration":   func(p map[string]any) { p["duration_ms"] = "1" },
		"null duration":     func(p map[string]any) { p["duration_ms"] = nil },
		"started outcome":   func(p map[string]any) { p["status"] = "started"; delete(p, "duration_ms") },
		"started duration":  func(p map[string]any) { p["status"] = "started"; delete(p, "outcome") },
	} {
		t.Run(name, func(t *testing.T) {
			value := jsonValue(t, fixture(t, "conversation/tool_finished_ok")).(map[string]any)
			change(value["payload"].(map[string]any))
			data, _ := json.Marshal(value)
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err == nil {
				t.Fatal("strict codec accepted invalid activity")
			}
			var client ClientDecoder
			if got, err := client.Decode(data); err == nil || got != nil {
				t.Fatal("client accepted invalid activity")
			}
		})
	}
	for _, field := range []string{"arguments", "output"} {
		value := jsonValue(t, fixture(t, "conversation/tool_started")).(map[string]any)
		value["payload"].(map[string]any)[field] = "private tool data"
		data, _ := json.Marshal(value)
		var envelope Envelope
		if err := json.Unmarshal(data, &envelope); err == nil {
			t.Fatalf("strict codec accepted %s", field)
		}
	}
	for _, payload := range []map[string]any{{"phase": "thinking"}, {"phase": nil}, {"tool_name": 1}} {
		data, _ := json.Marshal(clientMessage(t, "turn_state", payload))
		var client ClientDecoder
		if got, err := client.Decode(data); err == nil || got != nil {
			t.Fatalf("accepted invalid turn state: %s", data)
		}
	}
}

func TestToolActivityEncodingAndExactDuration(t *testing.T) {
	var envelope Envelope
	if err := json.Unmarshal(fixture(t, "conversation/tool_finished_ok"), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, number := range []json.Number{"0", "0.125", "9007199254740993.125"} {
		activity := envelope.Payload.(ToolActivity)
		activity.DurationMs = &number
		envelope.Payload = activity
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		var client ClientDecoder
		got, err := client.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if duration := got.Payload.(ToolActivity).DurationMs; duration == nil || *duration != number {
			t.Fatalf("duration rounded: %v, want %s", duration, number)
		}
	}
	for name, mutate := range map[string]func(*ToolActivity){
		"negative duration":  func(p *ToolActivity) { n := json.Number("-1"); p.DurationMs = &n },
		"missing duration":   func(p *ToolActivity) { p.DurationMs = nil },
		"missing outcome":    func(p *ToolActivity) { p.Outcome = nil },
		"unknown outcome":    func(p *ToolActivity) { outcome := ToolActivityOutcome("unknown"); p.Outcome = &outcome },
		"unknown status":     func(p *ToolActivity) { p.Status = "unknown" },
		"started completion": func(p *ToolActivity) { p.Status = ToolActivityStatusStarted },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := envelope
			activity := candidate.Payload.(ToolActivity)
			mutate(&activity)
			candidate.Payload = activity
			if _, err := json.Marshal(candidate); err == nil {
				t.Fatal("encoded invalid activity")
			}
		})
	}
}

func TestClientAdditiveFieldsAndSnapshotKinds(t *testing.T) {
	for _, name := range []string{"welcome", "normal/snapshot", "conversation/history"} {
		t.Run(name, func(t *testing.T) {
			original := fixture(t, name)
			value := jsonValue(t, original).(map[string]any)
			payload := value["payload"].(map[string]any)
			payload["future_field"] = map[string]any{"nested": true}
			switch name {
			case "welcome":
				payload["agents"].([]any)[0].(map[string]any)["future_field"] = "ignored"
			case "normal/snapshot":
				items := payload["items"].([]any)
				items[0].(map[string]any)["payload"].(map[string]any)["future_field"] = true
				payload["items"] = append(items, map[string]any{"kind": "future_activity", "key": "future", "revision": 1, "payload": map[string]any{"future_field": true}})
			case "conversation/history":
				payload["snapshot"] = jsonValue(t, fixture(t, "normal/snapshot")).(map[string]any)["payload"]
				// Compare against a history with the same known snapshot.
				clean := jsonValue(t, original).(map[string]any)
				clean["payload"].(map[string]any)["snapshot"] = payload["snapshot"]
				original, _ = json.Marshal(clean)
				snapshot := payload["snapshot"].(map[string]any)
				snapshot["items"] = append(snapshot["items"].([]any), map[string]any{"kind": "future_activity", "key": "future", "revision": 1, "payload": map[string]any{}})
			}
			data, _ := json.Marshal(value)
			var client ClientDecoder
			got, err := client.Decode(data)
			if err != nil || got == nil {
				t.Fatalf("additive payload rejected: %v", err)
			}
			encoded, err := json.Marshal(got)
			if err != nil || !reflect.DeepEqual(jsonValue(t, encoded), jsonValue(t, original)) {
				t.Fatalf("known fields changed: %s, %v", encoded, err)
			}
			if client.Stats() != (DecodeStats{Accepted: 1}) {
				t.Fatalf("snapshot items must not count as envelopes: %+v", client.Stats())
			}
			var strict Envelope
			if err := json.Unmarshal(data, &strict); err == nil {
				t.Fatal("strict codec accepted additive data")
			}
		})
	}
}

func TestUnknownSnapshotItemStillRequiresValidMetadata(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"missing key":      func(p map[string]any) { delete(p, "key") },
		"invalid revision": func(p map[string]any) { p["revision"] = 0 },
		"empty kind":       func(p map[string]any) { p["kind"] = "" },
		"null payload":     func(p map[string]any) { p["payload"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			value := jsonValue(t, fixture(t, "normal/snapshot")).(map[string]any)
			item := value["payload"].(map[string]any)["items"].([]any)[0].(map[string]any)
			item["kind"] = "future_activity"
			mutate(item)
			data, _ := json.Marshal(value)
			var client ClientDecoder
			if got, err := client.Decode(data); err == nil || got != nil || client.Stats() != (DecodeStats{Malformed: 1}) {
				t.Fatalf("invalid unknown item accepted: %v, %v", got, err)
			}
		})
	}
}
