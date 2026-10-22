// SPDX-License-Identifier: Apache-2.0
package runs

import "context"

// TransferNotices hands each pending generation to a durable conversation or
// home-card outbox. accept must acknowledge a committed, idempotent enqueue and
// must not call this Service. A failed transfer leaves the source pending.
// The service lock prevents an answer/new landing racing acknowledgement of
// an earlier needs-input generation. Delivery still belongs to the lane.
func (s *Service) TransferNotices(ctx context.Context, conversation string, accept func(context.Context, Notice) error) error {
	if accept == nil {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	notices, err := s.Notices(ctx, conversation)
	if err != nil {
		return err
	}
	for _, n := range notices {
		if err := accept(ctx, n); err != nil {
			return err
		}
		r, err := s.read(ctx, n.ID)
		if err != nil {
			return err
		}
		if n.EffectQuestion {
			r.EffectsQuestion.Noticed = true
		} else {
			r.Noticed = true
		}
		if err := s.save(ctx, r, false); err != nil {
			return err
		}
	}
	return nil
}
