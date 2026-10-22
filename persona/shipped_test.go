// SPDX-License-Identifier: Apache-2.0
package persona_test

import (
	"github.com/thisisjun786/lina/persona"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"
)

func TestShippedWorkingPairs(t *testing.T) {
	for _, key := range []string{"system", "lina", "dev", "knowledge", "life", "creative", "investing", "legal"} {
		for _, layer := range []string{"core-v1.md", "default-voice-v1.md"} {
			data, err := os.ReadFile(filepath.Join(key, layer))
			if err != nil || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
				t.Errorf("%s/%s is not a usable shipped layer: %v", key, layer, err)
			}
		}
	}
}

func TestCatalogAndIndependentPairBudgets(t *testing.T) {
	want := map[string]string{"system": "Selene", "lina": "Lina", "dev": "Sion", "knowledge": "Rumi", "life": "Arin", "creative": "Cleo", "investing": "Kai", "legal": "Kanna"}
	source := fstest.MapFS{}
	for _, s := range persona.Starters() {
		if want[s.Key] != s.Name {
			t.Fatalf("unexpected starter: %+v", s)
		}
		delete(want, s.Key)
		source[s.Key+"/core-v1.md"] = &fstest.MapFile{Data: []byte(strings.Repeat("x", 32768))}
		source[s.Key+"/default-voice-v1.md"] = &fstest.MapFile{Data: []byte(strings.Repeat("y", 32768))}
	}
	if len(want) != 0 {
		t.Fatal("missing starters", want)
	}
	if err := persona.Validate(source); err != nil {
		t.Fatal("pair budgets were aggregated", err)
	}
	source["legal/default-voice-v1.md"].Data = []byte(strings.Repeat("y", 32769))
	if err := persona.Validate(source); err == nil {
		t.Fatal("oversized pair accepted")
	}
	if c, v, err := persona.Load(source, "dev"); err != nil || c == "" || v == "" {
		t.Fatal("another preset affected")
	}
	if c, v, err := persona.Load(source, "../dev"); err == nil || c != "" || v != "" {
		t.Fatal("unknown key accepted")
	}
	if err := persona.Validate(persona.Shipped()); err != nil {
		t.Fatal(err)
	}
}
