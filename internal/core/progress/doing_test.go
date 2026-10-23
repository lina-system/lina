// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/work"
	"testing"
)

func TestDoingLineFromWorkerRecords(t *testing.T) {
	var c work.Card
	if err := json.Unmarshal([]byte(`{"task":{"id":"task","title":"Repair login"},"state":"working","active":true,"run":{"id":"run"},"doing":{"line":"Login repair · Running tests","busy":true,"since":"2026-10-23T00:00:00Z"}}`), &c); err != nil {
		t.Fatal(err)
	}
	units := UnplannedWork(map[string]work.Card{"task": c})
	if len(units) != 1 || units[0].Now != "Login repair · Running tests" {
		t.Fatalf("worker record did not reach card: %+v", units)
	}
	c.State = "input_needed"
	if got := UnplannedWork(map[string]work.Card{"task": c})[0]; got.Now == units[0].Now || got.Waiting != 1 {
		t.Fatal("worker title hid a person request", got)
	}
}
