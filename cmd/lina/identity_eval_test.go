// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/messenger/identity"
)

// TestPrivateIdentityEvalRejectsAShortSample proves the private command does
// not certify a sample that has not reached the owner-sample minimum.
func TestPrivateIdentityEvalRejectsAShortSample(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("people.json", []identity.SetPerson{{
		ID: "record-1", Names: []string{"Synthetic Person"},
		Contacts: []identity.SetContact{{Kind: identity.Phone, Value: "+821012345678", Source: "owner sample"}},
	}})
	write("identities.json", []identity.SetIdentity{{
		Platform: "imessage", User: "+821012345678", At: "2026-10-01T09:00:00Z",
		Names:       []string{"Synthetic Person"},
		Identifiers: []identity.SetContact{{Kind: identity.Phone, Value: "+821012345678", Source: "owner sample"}},
	}})
	write("labels.json", map[string]string{"imessage//+821012345678": "record-1"})

	counts := filepath.Join(dir, "counts.json")
	var stdout string
	code := 0
	func() {
		old := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		defer func() {
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			stdout = buf.String()
		}()
		code = runIdentityEval([]string{"--private", dir, "--counts", counts})
	}()
	if code == 0 {
		t.Fatal("one correct usable link passed the private gate")
	}
	if !strings.Contains(stdout, "gate false") {
		t.Fatalf("report = %q", stdout)
	}
	data, err := os.ReadFile(counts)
	if err != nil {
		t.Fatal(err)
	}
	var written map[string]any
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatal(err)
	}
	if written["gate"] != false || written["usable_links"] != float64(1) {
		t.Fatalf("counts = %s", data)
	}
}
