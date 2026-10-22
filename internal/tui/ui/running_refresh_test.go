// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Exercise the same decoded wire frames as a connected TUI, including snapshot
// replacement and replay, without depending on a provider or a polling clock.
func runningRefreshFrame(t *testing.T, text string, seq json.Number, snapshot bool) state.Frame {
	t.Helper()
	var envelope protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &envelope); err != nil {
		t.Fatal(err)
	}
	update := protocol.RunningUpdate{Text: &text}
	cursor := protocol.StreamCursor{Epoch: envelope.Epoch, Seq: seq}
	if snapshot {
		body, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		envelope.Payload = protocol.Snapshot{Topic: "running", Cursor: cursor, RevocationEpoch: "0",
			Items: []protocol.StreamItem{{Kind: protocol.KindRunningUpdate, Key: "run-fixture", Revision: seq, Payload: body}}}
	} else {
		envelope.Kind = protocol.KindRunningUpdate
		update.Stream = &protocol.StreamMetadata{Topic: "running", Key: "run-fixture", Cursor: cursor, Revision: seq}
		envelope.Payload = update
	}
	return streamFrame(t, envelope)
}

func TestRunningPagerAppliesCompletionWhileOpen(t *testing.T) {
	for _, width := range []int{80, 140} {
		for _, snapshot := range []bool{false, true} {
			t.Run(fmt.Sprintf("width=%d/snapshot=%t", width, snapshot), func(t *testing.T) {
				m := conversationShell()
				m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
				m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running")})
				m.Update(tea.PasteMsg{Content: "DRAFT-433"})
				initial := runningRefreshFrame(t, "in_progress: RUN-433", "1", true)
				m.Update(shellData{Generation: 1, Message: initial})
				m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
				if !m.conversation.pager.open || !strings.Contains(m.View().Content, "in_progress: RUN-433") {
					t.Fatal("Ctrl+R did not show the running row")
				}
				// Keep an active search to establish that updates retain input focus.
				m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
				m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
				before := m.conversation.pager
				completed := runningRefreshFrame(t, "completed: RUN-433", "2", snapshot)
				_, command := m.Update(shellData{Generation: 1, Message: completed})
				rows := m.topicBlocks("running")
				if len(rows) != 1 || rows[0].Text != "completed: RUN-433" {
					t.Fatalf("completion did not reach the model: %+v", rows)
				}
				view := m.View().Content
				if strings.Contains(view, "in_progress: RUN-433") || !strings.Contains(view, "completed: RUN-433") {
					t.Fatalf("open pager retained the unfinished row after completion: %q", m.conversation.pager.text)
				}
				p := m.conversation.pager
				if !p.open || !p.searching || p.query != before.query || p.offset != before.offset || m.content.Draft.Text != "DRAFT-433" || command != nil {
					t.Fatal("completion moved focus, changed reading/input state, or scheduled an effect")
				}
				var output bytes.Buffer
				d := &display{output: &output, interactive: true, styles: m.styles}
				if err := d.showShell(m); err != nil {
					t.Fatal(err)
				}
				output.Reset()
				m.Update(shellData{Generation: 1, Message: completed})
				m.Update(shellData{Generation: 1, Message: initial})
				if err := d.showShell(m); err != nil {
					t.Fatal(err)
				}
				if output.Len() != 0 || m.View().Content != view {
					t.Fatal("duplicate completion or an older snapshot changed the pager")
				}
			})
		}
	}
}
