// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationAnchorOnMessageBoundaries(t *testing.T) {
	for offset := 1; offset <= 34; offset++ {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
			var blocks []state.Block
			for i := 0; i < 50; i++ {
				blocks = append(blocks, state.Block{ID: fmt.Sprint(i), Text: fmt.Sprintf("Observation %02d", i), Done: true, Revision: 1, Speaker: protocol.ConversationSpeakerSystem})
			}
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: blocks}})
			m.Update(tea.PasteMsg{Content: "Keep this draft"})
			m.View()
			m.scrollConversation(offset)
			before := conversationRows(m)
			m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: "conversation:fixture", Cursor: 1, Block: state.Block{ID: "new", Text: "New worker question", Done: true, Revision: 1, Speaker: protocol.ConversationSpeakerSystem}}})
			if after := conversationRows(m); !reflect.DeepEqual(before, after) {
				t.Fatalf("new observation moved reading position:\n%v\n%v", before, after)
			}
			if m.content.Draft.Text != "Keep this draft" {
				t.Fatal("draft changed")
			}
		})
	}
}
