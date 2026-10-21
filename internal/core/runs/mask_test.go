// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"encoding/json"
	"github.com/thisisjun786/lina/kit/secret"
	"strings"
	"testing"
)

func TestRejectedMaskInputCannotBypassValidationFromCache(t *testing.T) {
	nested := strings.Repeat("[", 70) + `"password=synthetic-value"` + strings.Repeat("]", 70)
	body, _ := json.Marshal(map[string]string{"body": nested})
	var m masker
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := m.json(body); err == nil {
			t.Fatalf("attempt %d accepted unvalidated cached input", attempt)
		}
	}
}

func TestNestedCredentialJSONRemainsValidAfterMasking(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"nested": `{"token":"synthetic-nested"}`})
	var m masker
	masked, err := m.json(body)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]string
	if err := json.Unmarshal(masked, &record); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(masked), "synthetic-nested") || !json.Valid([]byte(record["nested"])) {
		t.Fatalf("nested credential data was retained or corrupted: %s", masked)
	}
}

func TestSensitiveJSONFieldsMaskEveryValueType(t *testing.T) {
	var m masker
	for _, raw := range []string{`{"password":123456}`, `{"token":[123456,"synthetic-value"]}`, `{"api_key":{"value":"synthetic-value"}}`, `{"client_secret":true}`} {
		body, err := m.json([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for _, value := range fields {
			if value != secret.Placeholder {
				t.Errorf("credential field retained its value: %s", body)
			}
		}
	}
}
