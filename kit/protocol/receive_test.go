// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"testing"
)

func TestClientCountsUnknownKindAndContinues(t *testing.T) {
	var client ClientDecoder
	unknown := jsonValue(t, fixture(t, "welcome")).(map[string]any)
	unknown["kind"] = "future_event"
	unknown["payload"] = map[string]any{"future_field": true}
	data, _ := json.Marshal(unknown)
	for range 2 {
		got, err := client.Decode(data)
		if err != nil || got != nil {
			t.Fatalf("unknown kind must be ignored: got=%v err=%v", got, err)
		}
	}
	got, err := client.Decode(fixture(t, "welcome"))
	if err != nil || got == nil || got.Kind != KindWelcome {
		t.Fatalf("known frame after unknown kind: got=%v err=%v", got, err)
	}
	if got := client.Stats(); got != (DecodeStats{Accepted: 1, UnknownKinds: 2}) {
		t.Fatalf("counts: %+v", got)
	}
	// Client tolerance must not weaken the generated codec used by servers.
	var strict Envelope
	if err := json.Unmarshal(data, &strict); err == nil {
		t.Fatal("strict codec accepted an unknown kind")
	}
}

func TestClientMalformedFramesAreNotUnknownKinds(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing kind":                      func(v map[string]any) { delete(v, "kind") },
		"null kind":                         func(v map[string]any) { v["kind"] = nil },
		"empty kind":                        func(v map[string]any) { v["kind"] = "" },
		"numeric kind":                      func(v map[string]any) { v["kind"] = 42 },
		"unknown envelope field":            func(v map[string]any) { v["extra"] = true },
		"unknown known-payload field":       func(v map[string]any) { v["payload"].(map[string]any)["extra"] = true },
		"future kind missing envelope":      func(v map[string]any) { v["kind"] = "future_event"; delete(v, "epoch") },
		"future kind bad version":           func(v map[string]any) { v["kind"] = "future_event"; v["protocol"].(map[string]any)["version"] = "2.0" },
		"future kind bad payload":           func(v map[string]any) { v["kind"] = "future_event"; v["payload"] = nil },
		"future kind extra field":           func(v map[string]any) { v["kind"] = "future_event"; v["extra"] = true },
		"future kind refusal without error": func(v map[string]any) { v["kind"] = "future_event"; v["effect_state"] = "refused" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := jsonValue(t, fixture(t, "welcome")).(map[string]any)
			mutate(value)
			data, _ := json.Marshal(value)
			var client ClientDecoder
			got, err := client.Decode(data)
			if err == nil || got != nil || client.Stats() != (DecodeStats{Malformed: 1}) {
				t.Fatalf("got=%v err=%v counts=%+v", got, err, client.Stats())
			}
		})
	}
	for _, data := range [][]byte{[]byte(`{"kind":"future_event"`), append(fixture(t, "welcome"), []byte(` {}`)...)} {
		var client ClientDecoder
		if got, err := client.Decode(data); err == nil || got != nil || client.Stats() != (DecodeStats{Malformed: 1}) {
			t.Fatalf("accepted malformed JSON: got=%v err=%v", got, err)
		}
	}
}
