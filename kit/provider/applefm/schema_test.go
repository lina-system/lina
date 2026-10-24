// SPDX-License-Identifier: Apache-2.0

package applefm

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestIntegerFieldsAcceptSignedNumbersOnly(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"n":{"type":"integer"}},"required":["n"]}`)
	got, err := ValidateObject(schema, json.RawMessage(`{"n":-1}`))
	if err != nil || string(got) != `{"n":-1}` {
		t.Fatalf("object=%s err=%v", got, err)
	}
	for _, raw := range []string{`{"n":"12"}`, `{"n":1.5}`, `{"n":true}`} {
		if _, err := ValidateObject(schema, json.RawMessage(raw)); !errors.Is(err, ErrSchemaMiss) {
			t.Fatalf("%s err=%v", raw, err)
		}
	}
}
