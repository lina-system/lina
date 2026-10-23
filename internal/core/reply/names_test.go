// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"github.com/thisisjun786/lina/internal/core/memory"
	"testing"
)

func TestRetiredNameMatches(t *testing.T) {
	names := []memory.Item{{Name: &memory.NameValue{Current: "Birch", RetiredAliases: []string{"Cedar", "카페", "Café", "Ann"}}}}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"Cedar is ready. Cedar is ready.", true}, {"CEDAR is ready.", true}, {"Cedarwood is ready.", false},
		{"카페에서 봐요.", true}, {"Cafe\u0301", true}, {"Anna is ready.", false}, {"Birch is ready.", false},
	} {
		if got := UsesRetiredName(tc.text, names); got != tc.want {
			t.Errorf("%q: %v want %v", tc.text, got, tc.want)
		}
	}
	names[0].Name = &memory.NameValue{Current: "Cedar Pro", RetiredAliases: []string{"Cedar"}}
	if UsesRetiredName("Cedar Pro is ready.", names) {
		t.Fatal("current name counted as retired")
	}
	names[0].Name = &memory.NameValue{Current: "Cedar", RetiredAliases: []string{"Cedarwood"}}
	if !UsesRetiredName("Cedarwood is ready.", names) {
		t.Fatal("current prefix hid a retired name")
	}
}
