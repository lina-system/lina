// SPDX-License-Identifier: Apache-2.0
package work

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work/pin"
)

func TestPinnedManifestIdentity(t *testing.T) {
	manifest := []Artifact{{Path: "/b", SHA256: strings.Repeat("b", 64)}, {Path: "/a", SHA256: strings.Repeat("a", 64)}}
	// Independent MANIFEST-CANON-01 vector, including sorting and no final newline.
	const revision = "7044905c744e86fcf06e6e08c2f312d5bd21f5c9b5a18627fbc256a21ea3eb9f"
	if got := ManifestHash(manifest); got != revision {
		t.Fatalf("manifest revision %s", got)
	}
	r := Receipt{RelationshipID: "task-1", ExecutionGeneration: 1, Outcome: "ready_for_review", RevisionHash: revision}
	if got := ReceiptID(r); got != "829f863c39bbe6348cc162929fa0709d" {
		t.Fatalf("receipt identity %s", got)
	}
}

func TestReceiptExtendsPinnedShape(t *testing.T) {
	raw, err := os.ReadFile("pin/pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var record pin.Record
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	validator, err := pin.NewValidator(record, os.DirFS("../../../testdata/crw"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"ready_for_review", "failed", "interrupted", "blocked_needs_input"} {
		r := receiptFixture()
		r.Outcome = outcome
		if outcome != "ready_for_review" {
			r.Manifest = nil
			r.Provenance = nil
			r.RevisionHash = strings.Repeat("0", 64)
		}
		if outcome == "failed" || outcome == "interrupted" {
			r.TurnRef.TurnStatus = outcome
		}
		r.EventID = ReceiptID(r)
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, "schema_version")
		delete(fields, "provenance")
		data, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		valid, err := validator.Validate("completion-receipt", data)
		if err != nil || !valid {
			t.Fatalf("%s no longer maps by addition: %s %v", outcome, data, err)
		}
	}
}
