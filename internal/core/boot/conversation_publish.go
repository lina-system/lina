// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) publish(ctx context.Context, kind protocol.Kind, key string, payload any) error {
	if r.coordinator != nil {
		return r.coordinator.publish(ctx, kind, key, payload)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Keep durable first appearance and live publication in the same order,
	// including when input admission races the provider observer.
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	positioned := kind == protocol.KindItemDone || kind == protocol.KindOutputDelta || kind == protocol.KindResult || kind == protocol.KindBackgroundNotice
	if positioned {
		if !r.positioned[key] {
			// A committed outcome still publishes after its turn is cancelled.
			saveCtx := context.WithoutCancel(ctx)
			if err := r.db.Write(saveCtx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(saveCtx, `INSERT INTO conversation_order(conversation_id,item_key) VALUES(?,?) ON CONFLICT DO NOTHING`, r.conversation, key)
				return err
			}); err != nil {
				return err
			}
		}
	}
	agent := r.agent
	if kind == protocol.KindResult || kind == protocol.KindBackgroundNotice {
		// Results carry attribution in provenance, not an agent_id wire field.
		agent = ""
	}
	if err := r.topics.Publish("conversation:"+r.conversation, clientsrv.TopicEvent{Kind: kind, Key: key, Data: data, AgentID: agent}); err != nil {
		return err
	}
	if positioned {
		if r.positioned == nil {
			r.positioned = make(map[string]bool)
		}
		// A remembered position also proves successful live publication. A
		// failed publication must remain retryable even after its SQL commit.
		r.positioned[key] = true
	}
	return nil
}

// The installed conversation currently has no negotiated surface width or
// language setting. Use the Korean ceiling and the acceptance surface's 80
// columns, with the same wrapping as the TUI. Keep policy in the existing filter.
func conversationFilter(shapes ...reply.Shapes) (*reply.Filter, error) {
	var method *reply.Shapes
	if len(shapes) > 0 {
		method = &shapes[0]
	}
	return reply.NewFilter(conversationReplyConfig(method))
}

func conversationReplyConfig(method *reply.Shapes) reply.Config {
	return reply.Config{Shapes: method, Budget: reply.ChatCeiling(), Language: "ko", RenderLines: func(text string) int {
		if text == "" {
			return 0
		}
		stream := render.NewStream("reply", render.Config{Width: 80}, nil)
		stream.Seal(1, text)
		return stream.Len()
	}}
}

// Keep possible credential prefixes private until the admission masker can
// recognize their complete shape. Mask the accumulated prefix so assignment
// and authorization context survives provider delta boundaries. Quoted values,
// structured JSON and PEM blocks wait for the authoritative final item rather
// than exposing an incomplete value or private-key header.
type conversationReplyStream struct {
	filter *reply.Filter
	raw    strings.Builder
	sent   string
}

func (s *conversationReplyStream) Push(delta string) (reply.View, string, error) {
	if !utf8.ValidString(delta) {
		return reply.View{}, "", reply.ErrUTF8
	}
	s.raw.WriteString(delta)
	text := s.raw.String()
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return reply.View{}, "", nil
	}
	if start := strings.Index(text, "-----"); start >= 0 {
		text = text[:start]
	}
	// A quoted assignment can contain whitespace or escaped delimiters. Do not
	// expose any of it before the complete item is available to the masker.
	if start := strings.IndexAny(text, "\"'"); start >= 0 {
		text = text[:start]
	}
	end := len(text)
	// Shape detectors can match inside a word. Hold from any complete or
	// unfinished introducer in the last token, including a split PEM marker.
	// These introducers correspond to secrets.Mask's credential shapes; the
	// masker itself remains the authority for replacing complete values.
	tokenStart := strings.LastIndexAny(text, " \t\r\n") + 1
	for i := tokenStart; i < end; i++ {
		for _, prefix := range []string{"sk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "AIza", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "AKIA", "ASIA", "eyJ", "-----"} {
			if strings.HasPrefix(text[i:], prefix) || strings.HasPrefix(prefix, text[i:]) {
				end = i
				break
			}
		}
	}
	masked, err := secrets.Mask(text[:end])
	if err != nil {
		return reply.View{}, "", err
	}
	if !strings.HasPrefix(masked.Text(), s.sent) {
		return reply.View{}, "", errors.New("reply masking changed a published prefix")
	}
	next := masked.Text()[len(s.sent):]
	if next == "" {
		return reply.View{}, "", nil
	}
	s.sent = masked.Text()
	view, err := s.filter.Push(next)
	return view, next, err
}

func (r *conversationRuntime) replyCard(key, turnID string, view reply.View, language design.Language) reply.Card {
	return reply.Card{Preview: *view.Card, ResultID: "result:" + key, Revision: 1,
		Title: design.Text(language, "reply.full"), Summary: view.Text, RevisedBy: r.agent,
		Provenance: protocol.ResultProvenance{AgentID: r.agent, ConversationID: r.conversation, TurnID: turnID}}
}

const conversationPreviewBytes = 64 << 10

func (r *conversationRuntime) publishReply(ctx context.Context, key, turnID string, view reply.View, language design.Language) error {
	if view.Card != nil {
		card := r.replyCard(key, turnID, view, language)
		// Bound the wire preview independently of the complete ledger card.
		// Even JSON's worst-case six-byte escapes leave ample envelope space
		// below the RPC writer's 1 MiB limit.
		text := card.Text
		if len(text) > conversationPreviewBytes {
			end := conversationPreviewBytes
			for !utf8.RuneStart(text[end]) {
				end--
			}
			text = text[:end] + "\n… " + design.Text(language, "reply.truncated")
		}
		return r.publish(ctx, protocol.KindResult, key, protocol.Result{
			ResultID: card.ResultID, Revision: "1", Type: card.Type, Status: card.Status,
			Title: card.Title, Summary: card.Summary, Text: &text,
			RevisedBy: card.RevisedBy, Provenance: card.Provenance,
		})
	}
	speaker := protocol.ConversationSpeakerAgent
	return r.publish(ctx, protocol.KindItemDone, key, protocol.ItemDone{Text: &view.Text, Speaker: &speaker})
}

func (r *conversationRuntime) publishPartials(ctx context.Context, stopped turn.Stopped) error {
	for _, p := range stopped.Partials {
		// The provider observer numbers rounds from one, the loop from zero.
		key := fmt.Sprintf("%s:%d:%d", stopped.TurnID, p.Round+1, p.Index)
		if err := r.publishPartial(ctx, key, stopped.TurnID, p.Text); err != nil {
			return err
		}
	}
	return nil
}

// Both stopped-turn publication and startup history use the same reply filter,
// including card identity and provenance for a partial that exceeded the ceiling.
func (r *conversationRuntime) publishPartial(ctx context.Context, key, turnID, text string) error {
	filter, err := conversationFilter()
	if err != nil {
		return err
	}
	view, err := filter.Push(text)
	if err != nil {
		return err
	}
	language, err := r.turnLanguage(ctx, turnID)
	if err != nil {
		return err
	}
	return r.publishReply(ctx, key, turnID, view, language)
}
