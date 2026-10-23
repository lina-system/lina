// SPDX-License-Identifier: Apache-2.0
package result

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMockReferencesAndFallback(t *testing.T) {
	raw := json.RawMessage(`{"request":"Show open tasks","checklist":[{"id":"tasks","description":"Open tasks","action":"none","target":""}],"page":{"asset_id":"page-1","revision":1},"png":{"asset_id":"png-1","revision":1},"previous":null,"stop_reason":"all_passed","checks":[{"id":"elements","passed":true},{"id":"controls","passed":true},{"id":"tokens","passed":true},{"id":"network","passed":true},{"id":"errors","passed":true},{"id":"layout_390","passed":true},{"id":"layout_1280","passed":true}]}`)
	b, err := Validate(Type("mock"), raw, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	text := PlainText(b, nil)
	for _, want := range []string{"Show open tasks", "Open tasks", "page-1", "png-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("fallback missing %q", want)
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"revision":1`, `"revision":0`, 1),
		strings.Replace(string(raw), `"passed":true`, `"passed":false`, 1),
		strings.Replace(string(raw), `"all_passed"`, `"no_improvement"`, 1),
		strings.Replace(string(raw), `"request":`, `"html":"<script>fetch('/')</script>","request":`, 1),
	} {
		if _, err := Validate(Type("mock"), []byte(bad), Limits{}); err == nil {
			t.Error("accepted invalid mock")
		}
	}
}
