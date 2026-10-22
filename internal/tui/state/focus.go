// SPDX-License-Identifier: Apache-2.0

package state

import "strings"

// FocusConversation drops only the prior conversation's display and send state.
// Core continues running its lane independently of this client's focus.
type FocusConversation struct{}

func (FocusConversation) isMsg() {}
func focusConversation(s State) State {
	blocks := make([]Block, 0, len(s.Blocks))
	for _, b := range s.Blocks {
		if !b.Local && !strings.HasPrefix(b.Topic, "conversation:") && !strings.HasPrefix(b.Topic, "results:") {
			blocks = append(blocks, b)
		}
	}
	s.Blocks = blocks
	topics := make(map[string]Topic)
	for name, t := range s.Topics {
		if !strings.HasPrefix(name, "conversation:") && !strings.HasPrefix(name, "results:") {
			topics[name] = t
		}
	}
	s.Topics = topics
	s.Busy, s.Stopping, s.TurnActive = false, false, false
	s.pending = nil
	s.Activity = Activity{}
	s.Worked = nil
	s.Gen++
	return s
}
