// SPDX-License-Identifier: Apache-2.0

package state

import "testing"

func TestMidTurnInputIsSteeringAndQueueIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		active, queue bool
		mode          InputMode
	}{{false, false, InputNormal}, {true, false, InputSteering}, {true, true, InputQueue}} {
		s, _ := Reduce(State{}, Turn{Active: tc.active})
		s, _ = Reduce(s, EditDraft{Draft: Draft{Text: "MESSAGE"}})
		sent, commands := Reduce(s, Send{Key: "input", Queue: tc.queue})
		if len(commands) != 1 {
			t.Fatal("input not submitted")
		}
		request := commands[0](t.Context()).(SendRequest)
		if request.Mode != tc.mode || !sent.Busy {
			t.Fatal("incorrect mid-turn routing")
		}
		sent, _ = Reduce(sent, EditDraft{Draft: Draft{Text: "NEW"}})
		restored, _ := Reduce(sent, SendResult{Gen: request.Gen, Key: request.Key, Failed: true})
		if restored.Draft.Text != "MESSAGE\nNEW" || len(restored.Blocks) != 0 || restored.Busy {
			t.Fatal("refused input was lost or echoed")
		}
	}
}
