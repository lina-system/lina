// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/kit/loop"
)

// EffectQuestion keeps the remainder that cannot run under a read-only scope
// visible to the conversation owner. It grants no authority: the work engine
// must bind concrete future effects to its normal authorization/approval path.
// Unknown receipts are evidence to reconcile, never calls to execute again.
type EffectQuestion struct {
	RunID, Turn, State string
	Effects            []budget.Effect
	Noticed            bool
	Answer             *EffectAnswer
}

// An answer is direction for the conversation, never a grant for an effect.
// Delivery to that conversation is an idempotent outbox in the existing record.
type EffectAnswer struct {
	Person, Action, Text string
	At                   time.Time
	Delivered            bool
}

func (s *Service) AnswerEffectQuestion(ctx context.Context, person, id, action, text string) error {
	if action != "accept" && action != "decline" && action != "cancel" || action == "accept" && strings.TrimSpace(text) == "" || action != "accept" && text != "" {
		return ErrInvalid
	}
	var err error
	text, err = masked(&s.masker, text)
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
	if person == "" || person != r.Origin.Person {
		return ErrScope
	}
	if r.EffectsQuestion == nil {
		return ErrState
	}
	q := r.EffectsQuestion
	if q.Answer != nil {
		if q.Answer.Person == person && q.Answer.Action == action && q.Answer.Text == text {
			return nil
		}
		return ErrState
	}
	q.Answer = &EffectAnswer{Person: person, Action: action, Text: text, At: s.cfg.Now().UTC(), Delivered: action != "accept"}
	q.State = "answered"
	return s.save(ctx, r, false)
}

func (s *Service) AcknowledgeEffectAnswer(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if r.EffectsQuestion == nil || r.EffectsQuestion.Answer == nil {
		return ErrState
	}
	r.EffectsQuestion.Answer.Delivered = true
	return s.save(ctx, r, false)
}

// EffectQuestions supplies pending question cards at the conversation boundary
// even while the read-only run continues. Delivery is independent of landing.
func (s *Service) EffectQuestions(ctx context.Context, conversation string) ([]EffectQuestion, error) {
	rows, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	var questions []EffectQuestion
	for _, r := range rows {
		if r.Origin.Conversation == conversation && r.EffectsQuestion != nil && !r.EffectsQuestion.Noticed {
			questions = append(questions, *r.EffectsQuestion)
		}
	}
	return questions, nil
}

// AcknowledgeEffectQuestion acknowledges delivery, never approval. It neither
// changes scope nor clears the question or its unknown receipts.
func (s *Service) AcknowledgeEffectQuestion(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if r.EffectsQuestion == nil {
		return ErrState
	}
	r.EffectsQuestion.Noticed = true
	return s.save(ctx, r, false)
}

// StartFromTurn durably admits the whole remaining request with gathered state.
// Like Start, it returns a handle before any model call. The caller persists or
// publishes that handle before Release. A repeated promotion returns the same
// run, including after restart; changing its gathered state is refused.
func (s *Service) StartFromTurn(ctx context.Context, o Origin, b Brief, h budget.Handoff) (budget.Handle, error) {
	if s.cfg.PublishPromotion == nil || o.Job != "" || h.Turn == "" || h.Turn != o.Turn || h.Input == "" && len(h.Items) == 0 {
		return budget.Handle{}, ErrInvalid
	}
	for _, item := range h.Items {
		if !json.Valid(item) {
			return budget.Handle{}, ErrInvalid
		}
	}
	for _, effect := range h.Effects {
		if effect.CallID == "" || effect.Key == "" || effect.Tool == "" || effect.Code != loop.OK && effect.Code != loop.Unknown {
			return budget.Handle{}, ErrInvalid
		}
	}
	data, err := json.Marshal(h)
	if err != nil {
		return budget.Handle{}, ErrInvalid
	}
	var detached budget.Handoff
	if err := json.Unmarshal(data, &detached); err != nil {
		return budget.Handle{}, ErrInvalid
	}
	// Promotion gets only reads from the caller's allowlist. It never inherits
	// an effect-capable conversation tool, scratch tool or hand-off tool.
	allow := make([]string, 0, len(b.Allow))
	for _, name := range b.Allow {
		if tool, ok := s.cfg.Tools[name]; ok && tool.Kind == Read && name != "start_run" && name != "dispatch_work" {
			allow = append(allow, name)
		}
	}
	b.Allow = allow
	id, state, err := s.start(ctx, o, b, &detached)
	if err != nil {
		return budget.Handle{}, err
	}
	return budget.Handle{ID: id, State: string(state)}, nil
}

func samePromotion(r Record, o Origin) bool {
	return r.Promotion != nil && r.Origin.Person == o.Person && r.Origin.Agent == o.Agent && r.Origin.Conversation == o.Conversation && r.Origin.Turn == o.Turn
}

func sameGathered(r Record, b Brief, h *budget.Handoff) bool {
	return r.PromotionDigest == promotionDigest(b, h)
}

// Compare original admission bytes by digest: persistence masks credential-like
// values, and neither masked collisions nor masking itself may change identity.
func promotionDigest(b Brief, h *budget.Handoff) string {
	current, _ := json.Marshal(struct {
		Brief Brief
		State *budget.Handoff
	}{b, h})
	digest := sha256.Sum256(current)
	return hex.EncodeToString(digest[:])
}
