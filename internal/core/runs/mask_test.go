// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"encoding/json"
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
