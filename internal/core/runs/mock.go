// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"
)

func (s *Service) drawMock(ctx context.Context, r Record) error {
	if s.cfg.DrawMock == nil {
		return ErrInvalid
	}
	if err := s.cfg.Authorize(ctx, r.Origin, r.Brief); err != nil {
		return err
	}
	s.activity(r.ID, 1)
	defer s.activity(r.ID, -1)
	artifact, reason, drawErr := s.cfg.DrawMock(ctx, r, func(raw json.RawMessage) error {
		return s.update(ctx, r.ID, func(current *Record) error {
			current.MockState = append(json.RawMessage(nil), raw...)
			current.LastEvent = s.cfg.Now().UnixNano()
			return nil
		})
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	current, err := s.read(context.Background(), r.ID)
	if err != nil {
		return err
	}
	if current.State == Landed {
		return nil
	}
	if current.PendingOutcome == Cancelled {
		return s.land(context.Background(), &current, Cancelled)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	outcome := Complete
	if drawErr != nil {
		outcome = Failed
		reason = "mock_unavailable"
		artifact = Artifact{Body: "The mock could not be drawn.", Missing: []string{"The renderer or model was unavailable."}}
	} else if !artifact.Done {
		outcome = Partial
	}
	current.Artifact, current.Reason = artifact, reason
	return s.land(context.Background(), &current, outcome)
}
