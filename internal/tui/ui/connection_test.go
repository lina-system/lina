// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConnectionCauseAndRetryMetadata(t *testing.T) {
	styles := NewStyles(Options{Profile: Monochrome, Language: design.Korean, Width: 100})
	for _, code := range []protocol.ErrorCode{protocol.ErrorUnavailable, protocol.ErrorUnsupportedCombination, protocol.ErrorNotAuthorized} {
		status := ConnectionStatus{State: Refused, Cause: code}
		view := styles.presentation(status)
		if !strings.Contains(view.Detail, string(code)) || !strings.Contains(view.Detail, design.Text(design.Korean, string(code))) {
			t.Fatal("typed cause missing")
		}
	}
	view := styles.presentation(ConnectionStatus{State: Retrying, Attempt: 3, NextTry: 2 * time.Second})
	if !strings.Contains(view.Detail, "3") || !strings.Contains(view.Detail, "2s") {
		t.Fatal("retry metadata missing")
	}
	view = styles.presentation(ConnectionStatus{State: Failed, Cause: "\x1b]52;bad\a"})
	if strings.ContainsAny(view.Detail, "\x1b\a") || strings.Contains(view.Detail, "52;") {
		t.Fatal("unknown cause exposed")
	}
	var output strings.Builder
	d := display{output: &output, styles: styles, interactive: true}
	first := ConnectionStatus{State: Refused, Cause: protocol.ErrorUnavailable}
	if err := d.showStatus(first); err != nil {
		t.Fatal(err)
	}
	n := output.Len()
	if err := d.showStatus(first); err != nil {
		t.Fatal(err)
	}
	if output.Len() != n {
		t.Fatal("unchanged status wrote bytes")
	}
	first.Cause = protocol.ErrorNotAuthorized
	if err := d.showStatus(first); err != nil {
		t.Fatal(err)
	}
	if output.Len() == n {
		t.Fatal("changed cause did not refresh")
	}
}

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
