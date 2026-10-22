// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

func TestValidatorRunsPinnedSchema(t *testing.T) {
	r, f := testRecord()
	v, err := NewValidator(r, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input string
		valid bool
	}{{`{}`, true}, {`false`, false}, {`[]`, false}} {
		got, err := v.Validate("relationship", json.RawMessage(tc.input))
		if err != nil || got != tc.valid {
			t.Fatalf("input=%s valid=%t err=%v", tc.input, got, err)
		}
	}
	if _, err := v.Validate("absent", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown schema accepted")
	}
	if _, err := v.Validate("relationship", json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed JSON treated as a judgment")
	}
}

func TestValidatorRefusesUnpinnedReferences(t *testing.T) {
	r, f := testRecord()
	for _, ref := range []string{"https://example.invalid/schema.json", "file:///unadopted.json"} {
		data, _ := json.Marshal(map[string]string{"$ref": ref})
		f[r.Schemas[0].Path].Data = data
		r.Schemas[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		if _, err := NewValidator(r, f); err == nil {
			t.Fatal("unpinned schema reference accepted")
		}
	}
}
