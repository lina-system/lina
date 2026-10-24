// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"testing"
)

func TestClientProgressDecodeCostStaysNearStrictCodec(t *testing.T) {
	value := jsonValue(t, fixture(t, "normal/progress_update")).(map[string]any)
	payload := value["payload"].(map[string]any)
	row := payload["rows"].([]any)[0].(map[string]any)
	stage := row["line"].(map[string]any)["stages"].([]any)[0].(map[string]any)
	step := stage["steps"].([]any)[0]
	steps := make([]any, 20)
	for i := range steps {
		steps[i] = step
	}
	stage["steps"] = steps
	rows, order := make([]any, 50), make([]string, 50)
	for i := range rows {
		current := maps.Clone(row)
		order[i] = fmt.Sprintf("goal-%d", i)
		current["ref"] = order[i]
		rows[i] = current
	}
	payload["rows"], payload["card_order"] = rows, order
	payload["counts"].(map[string]any)["running"] = 50
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var want Envelope
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	var client ClientDecoder
	got, err := client.Decode(raw)
	if err != nil || got == nil || !reflect.DeepEqual(*got, want) || client.Stats() != (DecodeStats{Accepted: 1}) {
		t.Fatalf("known progress changed: %v, stats=%+v", err, client.Stats())
	}
	strict := testing.AllocsPerRun(3, func() {
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
	})
	compatible := testing.AllocsPerRun(3, func() {
		var decoder ClientDecoder
		if _, err := decoder.Decode(raw); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("known progress: strict %.0f allocations, compatible %.0f", strict, compatible)
	if compatible > 2*strict {
		t.Fatalf("known frame repeatedly projected and decoded: %.0f allocations vs strict %.0f", compatible, strict)
	}
}
