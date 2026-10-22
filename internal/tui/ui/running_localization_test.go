// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func localizedRunningFrame(t *testing.T, fields map[string]any, seq json.Number, snapshot bool) state.Frame {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &envelope); err != nil {
		t.Fatal(err)
	}
	cursor := map[string]any{"epoch": envelope["epoch"], "seq": seq}
	if snapshot {
		envelope["payload"] = map[string]any{"topic": "running", "cursor": cursor, "revocation_epoch": "0",
			"items": []any{map[string]any{"kind": "running_update", "key": "run-fixture", "revision": seq, "payload": fields}}}
	} else {
		envelope["kind"] = "running_update"
		fields["stream"] = map[string]any{"topic": "running", "key": "run-fixture", "cursor": cursor, "revision": seq}
		envelope["payload"] = fields
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return state.Frame{Data: data}
}

func TestRunningStatesLocalizedInStripAndOpenPager(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, width := range []int{100, 140} {
			for _, snapshot := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/width=%d/snapshot=%t", language, width, snapshot), func(t *testing.T) {
					m := newShell(Options{Width: width, Profile: Monochrome, Language: language}, time.Now)
					m.Update(tea.WindowSizeMsg{Width: width, Height: 32})
					m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running")})
					m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
					m.Update(tea.PasteMsg{Content: "KEEP-DRAFT"})
					for i, code := range []string{"in_progress", "waiting", "needs_input", "ready_for_review", "failed", "stopped", "done", "unknown"} {
						// Text is deliberately inconsistent: only the typed state and
						// summary can determine the localized row, never a parsed prefix.
						frame := localizedRunningFrame(t, map[string]any{"run_id": "opaque-run", "state": code,
							"summary": "\x1b]52;c;secret\aSYNTHETIC", "text": "UNTRUSTED-LEGACY-TEXT"}, json.Number(fmt.Sprint(i+1)), snapshot)
						m.Update(shellData{Generation: 1, Message: frame})
						label, ok := design.Lookup(language, "running.state."+code)
						if !ok {
							t.Errorf("missing %s run state %s", language, code)
						}
						want := label + ": SYNTHETIC"
						if i == 0 {
							if view := m.View().Content; !strings.Contains(view, want) || strings.Contains(view, "UNTRUSTED-LEGACY-TEXT") {
								t.Errorf("strip does not use the localized typed row: %q", view)
							}
							m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
						}
						if view := m.conversation.pager.text; !strings.Contains(view, want) || strings.Contains(view, "UNTRUSTED-LEGACY-TEXT") || strings.Contains(view, "secret") {
							t.Errorf("open pager did not localize/sanitize %s: %q", code, view)
						}
						if m.content.Draft.Text != "KEEP-DRAFT" || !m.conversation.pager.open || m.topicBlocks("running")[0].RunID != "opaque-run" {
							t.Fatal("localization changed draft, focus or routing identity")
						}
					}
				})
			}
		}
	}
}

func TestRunningWaitingReasonsLocalized(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, reason := range []string{"dependency", "area", "quota", "host_capacity", "ci_pending", "run_limit", "person", "budget"} {
			t.Run(string(language)+"/"+reason, func(t *testing.T) {
				m := newShell(Options{Width: 100, Language: language}, time.Now)
				m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running")})
				m.Update(shellData{Generation: 1, Message: localizedRunningFrame(t, map[string]any{
					"state": "waiting", "summary": "SYNTHETIC", "reason": reason}, "1", true)})
				m.openRunning()
				label, ok := design.Lookup(language, "running.reason."+reason)
				if !ok || !strings.Contains(m.conversation.pager.text, "("+label+")") {
					t.Fatalf("waiting reason %s lacks its localized label: %q", reason, m.conversation.pager.text)
				}
			})
		}
	}
}

func TestRunningLegacyTextIsNotParsedAsStatus(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running")})
	text := "ready_for_review: arbitrary person-authored text"
	m.Update(shellData{Generation: 1, Message: localizedRunningFrame(t, map[string]any{"text": text}, "1", true)})
	rows := m.topicBlocks("running")
	if len(rows) != 1 || rows[0].Text != text || rows[0].Kind != protocol.KindRunningUpdate {
		t.Fatalf("legacy text was interpreted as a state code: %+v", rows)
	}
}
