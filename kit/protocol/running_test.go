// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"testing"
)

func TestRunningPresentationRoundTrips(t *testing.T) {
	for _, state := range []string{"in_progress", "waiting", "needs_input", "ready_for_review", "failed", "stopped", "done", "unknown"} {
		t.Run(state, func(t *testing.T) {
			roundTrip(t, clientMessage(t, "running_update", map[string]any{
				"run_id": "synthetic-run", "state": state, "summary": "Synthetic task", "text": "legacy text",
			}))
		})
	}
	for _, reason := range []string{"dependency", "area", "quota", "host_capacity", "ci_pending", "run_limit", "person", "budget"} {
		t.Run(reason, func(t *testing.T) {
			roundTrip(t, clientMessage(t, "running_update", map[string]any{
				"state": "waiting", "summary": "Synthetic task", "reason": reason,
			}))
		})
	}
	for _, legacy := range []map[string]any{{}, {"text": "Legacy activity"}, {"run_id": "synthetic-run", "removed": true}} {
		roundTrip(t, clientMessage(t, "running_update", legacy))
	}
}

func TestRunningPresentationRejectsMalformedFields(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"unknown state":   func(p map[string]any) { p["state"] = "untrusted_state" },
		"unknown reason":  func(p map[string]any) { p["reason"] = "untrusted_reason" },
		"numeric summary": func(p map[string]any) { p["summary"] = 1 },
		"missing state":   func(p map[string]any) { delete(p, "state") },
		"missing summary": func(p map[string]any) { delete(p, "summary") },
		"null summary":    func(p map[string]any) { p["summary"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			payload := map[string]any{"state": "waiting", "summary": "Synthetic task", "reason": "quota"}
			change(payload)
			data, err := json.Marshal(clientMessage(t, "running_update", payload))
			if err != nil {
				t.Fatal(err)
			}
			var strict Envelope
			if json.Unmarshal(data, &strict) == nil {
				t.Fatal("strict codec accepted malformed running presentation")
			}
			var client ClientDecoder
			if got, err := client.Decode(data); err == nil || got != nil {
				t.Fatal("client accepted malformed running presentation")
			}
		})
	}
}
