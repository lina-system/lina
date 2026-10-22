// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"os"
	"reflect"
	"testing"
)

func TestDecisionFocusEventsReachTheModel(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("\x1b[O\x1b[I")); writer.Close(); done <- err }()
	var focused []bool
	err = readConversationInput(t.Context(), reader, func(msg tea.Msg) bool {
		switch msg.(type) {
		case tea.BlurMsg:
			focused = append(focused, false)
		case tea.FocusMsg:
			focused = append(focused, true)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(focused, []bool{false, true}) {
		t.Fatalf("focus events=%v", focused)
	}
}
