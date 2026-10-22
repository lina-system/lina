// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type EmotionChange struct {
	Quote     string  `json:"quote"`
	Valence   float64 `json:"valence"`
	Arousal   float64 `json:"arousal"`
	Sensitive bool    `json:"sensitive,omitempty"`
}
type ExpressionChange struct {
	Quote   string `json:"quote"`
	Enabled bool   `json:"enabled"`
}
type ExpressionReceipt struct {
	agents.ExpressionResult
	Notice string `json:"notice,omitempty"`
}

var sensitiveEnglish = regexp.MustCompile(`(?i)\b(?:died|death|grief|grieving|funeral|medical|diagnosis|cancer|emergency|suicide|suicidal|assault|abuse|trauma|depression|self[- ]harm|panic attack|passed away)\b`)

// SensitivePersonaInput supplies a conservative immediate suppressor before
// the provider sees the turn. Admission tags cannot be cleared by model output.
// The writer can additionally mark a confirmed event as sensitive.
func SensitivePersonaInput(event ledger.Event) bool {
	for _, span := range event.Spans {
		if span.Sensitivity == "secret" || span.Sensitivity == "personal_id" || span.Sensitivity == "third_party" {
			return true
		}
	}
	text := event.Body.DerivedText()
	if sensitiveEnglish.MatchString(text) {
		return true
	}
	for _, word := range []string{"사망", "돌아가셨", "장례", "애도", "응급", "진단", "수술", "자해", "자살", "학대", "폭행"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func (p *Persona) RecordEmotion(ctx context.Context, scope PersonaScope, in EmotionChange) (agents.EmotionResult, error) {
	event, err := p.confirmed(ctx, scope, in.Quote)
	if err != nil {
		return agents.EmotionResult{}, err
	}
	return p.agents.RecordEmotion(ctx, scope.Agent, scope.StateRevision, agents.EmotionEvent{ConversationID: scope.Conversation, EventID: event.ID, Affect: agents.Affect{Valence: in.Valence, Arousal: in.Arousal}, At: p.now(), Sensitive: in.Sensitive || SensitivePersonaInput(event)}, scope.Person, p.emotionPolicy)
}
func (p *Persona) SetExpression(ctx context.Context, scope PersonaScope, in ExpressionChange) (ExpressionReceipt, error) {
	event, err := p.confirmed(ctx, scope, in.Quote)
	if err != nil {
		return ExpressionReceipt{}, err
	}
	result, err := p.agents.SetEmotionExpression(ctx, scope.Agent, scope.ExpressionRevision, in.Enabled, scope.Input, scope.Person)
	if err != nil {
		return ExpressionReceipt{}, err
	}
	receipt := ExpressionReceipt{ExpressionResult: result}
	if result.Changed {
		receipt.Notice = design.Text(design.ConversationLanguage(event.Body.Text(), design.Language(event.Surface.Language)), "notice.emotion_off")
		if result.Enabled {
			receipt.Notice = design.Text(design.ConversationLanguage(event.Body.Text(), design.Language(event.Surface.Language)), "notice.emotion_on")
		}
	}
	return receipt, nil
}

func RegisterEmotion(registry *tools.Registry, p *Persona, scope PersonaScope) ([]tools.Spec, error) {
	if registry == nil || p == nil || scope.Agent == "" || scope.StateRevision == "" || scope.ExpressionRevision == "" {
		return nil, tools.ErrInvalid
	}
	var specs []tools.Spec
	for _, name := range []string{"record_emotion", "set_emotion_expression"} {
		spec := tools.Spec{Classify: tools.Class(tools.PrivateDraft), Name: name, Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) {
			var value any
			target := "persona-state:" + scope.Agent
			what := "Record an emotional reaction"
			if name == "record_emotion" {
				var in struct {
					Quote     string   `json:"quote"`
					Valence   *float64 `json:"valence"`
					Arousal   *float64 `json:"arousal"`
					Sensitive bool     `json:"sensitive,omitempty"`
				}
				if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Quote) == "" || in.Valence == nil || in.Arousal == nil || *in.Valence < -1 || *in.Valence > 1 || *in.Arousal < -1 || *in.Arousal > 1 {
					return tools.Input{}, tools.ErrInvalid
				}
				value = EmotionChange{Quote: in.Quote, Valence: *in.Valence, Arousal: *in.Arousal, Sensitive: in.Sensitive}
			} else {
				var in struct {
					Quote   string `json:"quote"`
					Enabled *bool  `json:"enabled"`
				}
				if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Quote) == "" || in.Enabled == nil {
					return tools.Input{}, tools.ErrInvalid
				}
				value = ExpressionChange{Quote: in.Quote, Enabled: *in.Enabled}
				target = "persona-expression:" + scope.Agent
				what = "Set emotional expression"
			}
			args, err := json.Marshal(value)
			return tools.Input{Arguments: args, Target: target, What: what, Where: "This agent", ScopeKey: "persona", Write: true, Undoable: name == "set_emotion_expression"}, err
		}}
		err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, input tools.Input) (tools.Result, error) {
			var value any
			var revision string
			var err error
			if name == "record_emotion" {
				var in EmotionChange
				err = decodeSkill(input.Arguments, &in)
				if err == nil {
					var r agents.EmotionResult
					r, err = p.RecordEmotion(ctx, scope, in)
					value = r
					revision = r.Revision
				}
			} else {
				var in ExpressionChange
				err = decodeSkill(input.Arguments, &in)
				if err == nil {
					var r ExpressionReceipt
					r, err = p.SetExpression(ctx, scope, in)
					value = r
					revision = r.Revision
				}
			}
			if err != nil {
				return tools.Result{State: tools.Failed, Body: json.RawMessage(`{"code":"emotion_not_changed"}`)}, nil
			}
			body, err := json.Marshal(value)
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			return tools.Result{State: tools.Applied, Body: body, Revision: revision}, nil
		})
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func EmotionDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"record_emotion","description":"Record this agent's reaction only to a confirmed event in the current person's input. Quote the evidence. Never infer a fact or the person's emotion, change intimacy, or use an imagined event. Mark sensitive situations so expression stays suppressed. This writes only short emotion and mood, not disposition or memory.","parameters":{"type":"object","properties":{"quote":{"type":"string"},"valence":{"type":"number","minimum":-1,"maximum":1},"arousal":{"type":"number","minimum":-1,"maximum":1},"sensitive":{"type":"boolean"}},"required":["quote","valence","arousal"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"set_emotion_expression","description":"Turn this agent's emotional expression on or off when the person asks. Quote the current person's request. Stored emotion is retained; sensitive situations always suppress expression.","parameters":{"type":"object","properties":{"quote":{"type":"string"},"enabled":{"type":"boolean"}},"required":["quote","enabled"],"additionalProperties":false}}`),
	}
}
