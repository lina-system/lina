// SPDX-License-Identifier: Apache-2.0

package state

import (
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Activity and Worked are ephemeral display state, never conversation items.
type Activity struct {
	Started  time.Time
	ReplyID  string
	Replying bool
}

func (s *State) turn(m Turn) {
	if m.Active {
		if !s.TurnActive || !m.Started.IsZero() && !m.Started.Equal(s.Activity.Started) {
			s.Activity = Activity{Started: m.Started}
			s.Stopping = false
		}
	} else {
		if s.TurnActive && !s.Activity.Started.IsZero() && !m.At.IsZero() && s.Activity.ReplyID != "" {
			if s.Worked == nil {
				s.Worked = make(map[string]time.Duration)
			}
			s.Worked[s.Activity.ReplyID] = max(0, m.At.Sub(s.Activity.Started))
		}
		s.Activity = Activity{}
		s.Stopping = false
	}
	s.TurnActive = m.Active
}

func (s *State) activityBlock(b Block) {
	if b.Kind == protocol.KindTurnState && b.TurnKnown {
		s.turn(Turn{Active: b.TurnActive, Started: b.TurnStarted, At: b.TurnAt})
	} else if s.TurnActive && (b.Kind == protocol.KindOutputDelta || b.Speaker == protocol.ConversationSpeakerAgent || b.Kind == protocol.KindResult) {
		s.Activity.ReplyID = b.ID
		if b.Kind == protocol.KindOutputDelta && b.Text != "" {
			s.Activity.Replying = true
		}
	}
}
