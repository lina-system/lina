// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"strings"
	"time"
)

// InputAnswer is recorded with the run's question generation, not its mutable
// update timestamp. It survives later questions, landing and restart.
type InputAnswer struct {
	Revision              uint64
	Person, Agent, Answer string
	At                    time.Time
}

// AnswerInput accepts the person established by the authenticated Core entry.
// A source change and its attributed answer are one fenced run-record commit.
func (s *Service) AnswerInput(ctx context.Context, person, id string, revision uint64, value string) error {
	if person == "" || revision == 0 || strings.TrimSpace(value) == "" {
		return ErrInvalid
	}
	var err error
	value, err = masked(&s.masker, value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if r.Origin.Person != person {
		return ErrScope
	}
	for _, answer := range r.InputAnswers {
		if answer.Revision == revision {
			if answer.Person == person && answer.Answer == value {
				return nil
			}
			return ErrState
		}
	}
	if r.State != NeedsInput || r.NoticeVersion != revision {
		return ErrState
	}
	r.InputAnswers = append(r.InputAnswers, InputAnswer{Revision: revision, Person: person, Agent: r.Origin.Agent, Answer: value, At: s.cfg.Now().UTC()})
	r.FollowUps = append(r.FollowUps, value)
	r.Noticed = false
	r.State = Waiting
	r.Reason = ""
	if err := s.save(ctx, r, false); err != nil {
		return err
	}
	return s.schedule(ctx)
}
