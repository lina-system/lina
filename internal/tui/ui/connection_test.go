// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
)

func TestConnectionCatalogAndReadOnlyStates(t *testing.T) {
	seen := map[string]bool{}
	for _, state := range []Connection{Connecting, Live, Resyncing, Retrying, Refused, Recovering, Stopped, Failed, Quit} {
		id := state.Code()
		if seen[id] || id == "unknown" {
			t.Fatalf("invalid connection id %s", id)
		}
		seen[id] = true
		if state.CanMutate() != (state == Live) {
			t.Fatalf("mutation gate for %s", id)
		}
		for _, language := range []design.Language{design.English, design.Korean} {
			for _, key := range []string{id, "connection." + id} {
				if _, ok := design.Lookup(language, key); !ok {
					t.Errorf("missing %s:%s", language, key)
				}
			}
			view := localizedConnectionView(state, design.Dark(), false, language)
			if view.Label != design.Text(language, id) || view.Detail != design.Text(language, "connection."+id) {
				t.Fatalf("catalog not used for %s", id)
			}
		}
	}
	if Connection(255).CanMutate() || Connection(255).Code() != "unknown" {
		t.Fatal("unknown state permitted")
	}
}

func TestUnchangedConnectionWritesNothing(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		var output strings.Builder
		d := display{output: &output, styles: NewStyles(Options{Profile: Monochrome, Width: 80}), interactive: interactive}
		for _, state := range []Connection{Connecting, Live, Resyncing, Retrying, Recovering, Stopped, Refused, Failed, Quit} {
			if err := d.show(state); err != nil {
				t.Fatal(err)
			}
			before := output.Len()
			if err := d.show(state); err != nil {
				t.Fatal(err)
			}
			if output.Len() != before {
				t.Fatalf("unchanged %s wrote bytes", state.Code())
			}
		}
	}
}

func TestLanguageEnvironment(t *testing.T) {
	for _, env := range []map[string]string{
		{"LANG": "ko_KR.UTF-8"},
		{"LANG": "en_US.UTF-8", "LC_MESSAGES": "ko_KR.UTF-8"},
		{"LANG": "en_US.UTF-8", "LC_ALL": "ko_KR.UTF-8"},
	} {
		if environmentOptions(func(key string) string { return env[key] }).Language != design.Korean {
			t.Fatal("locale precedence")
		}
	}
	if environmentOptions(func(key string) string { return map[string]string{"LANG": "ko_KR.UTF-8", "LC_ALL": "C"}[key] }).Language != design.English {
		t.Fatal("LC_ALL did not override LANG")
	}
}
