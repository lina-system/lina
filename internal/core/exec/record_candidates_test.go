// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestCandidateSchemaCannotSupplyTrustedFacts(t *testing.T) {
	specs, err := RegisterRecordCandidates(tools.NewRegistry(nil, nil), &records.Store{}, records.Actor{Person: "owner", Agent: "agent"}, "current-event")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"addressed_to_person", "from_person", "request_or_promise", "person_id", "speaker", "source_revision", "class"} {
		raw := json.RawMessage(`{"source":"","title":"send notes","quotation":"I will send notes.","confidence":1,"` + field + `":true}`)
		if _, err := specs[0].Schema(raw); err == nil {
			t.Fatal("trusted field accepted", field)
		}
	}
	in, err := specs[0].Schema(json.RawMessage(`{"source":"","title":"send notes","quotation":"I will send notes.","confidence":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var got records.CandidateInput
	_ = json.Unmarshal(in.Arguments, &got)
	if got.Source != "current-event" || in.Target != "records:new" || !in.Write {
		t.Fatal(in, got)
	}
}
