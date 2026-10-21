// SPDX-License-Identifier: Apache-2.0

package spike_test

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/tui/spike"
	"strings"
	"testing"
)

const completeRecord = `{"format":1,"scenario":"S8","terminal":{"name":"emulator","version":"v0.1.0","term":"xterm-256color","ssh":false},"libraries":{"charm.land/bubbletea/v2":"v2.1.0","charm.land/bubbles/v2":"v2.2.1","charm.land/lipgloss/v2":"v2.0.6","charm.land/glamour/v2":"v2.0.1","github.com/charmbracelet/ultraviolet":"v0.0.0-20261008173134-6b8d4baf91b4","github.com/charmbracelet/x/vt":"v0.1.0"},"bounds":{"sleeps":0},"measured":{"sleeps":0},"result":"pass"}`

func TestEvidenceRequiresEveryField(t *testing.T) {
	if _, err := spike.ReadRecord(strings.NewReader(completeRecord)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"format", "scenario", "terminal", "terminal.name", "terminal.version", "terminal.term", "terminal.ssh", "libraries", "bounds", "measured", "result"} {
		t.Run(path, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(completeRecord), &doc); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(path, ".")
			target := doc
			if len(parts) == 2 {
				target = doc[parts[0]].(map[string]any)
			}
			delete(target, parts[len(parts)-1])
			data, _ := json.Marshal(doc)
			if _, err := spike.ReadRecord(strings.NewReader(string(data))); err == nil {
				t.Fatal("incomplete record accepted")
			}
		})
	}
	for _, bad := range []string{
		strings.Replace(completeRecord, `"ssh":false`, `"ssh":null`, 1),
		strings.Replace(completeRecord, `"measured":{"sleeps":0}`, `"measured":{"sleeps":null}`, 1),
		strings.Replace(completeRecord, `"scenario":"S8"`, `"scenario":"S12"`, 1),
		strings.Replace(completeRecord, `"result":"pass"`, `"result":"not_run"`, 1),
		strings.Replace(completeRecord, `"v2.1.0"`, `"latest"`, 1),
		completeRecord + completeRecord,
	} {
		if _, err := spike.ReadRecord(strings.NewReader(bad)); err == nil {
			t.Fatalf("invalid evidence accepted: %s", bad)
		}
	}
}
