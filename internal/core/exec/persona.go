// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

var ErrPersonaEvidence = errors.New("persona_requires_person_request")

// PersonaScope comes from the authenticated turn, never model arguments.
type PersonaScope struct {
	Agent, Person, Conversation, Input string
	Revision                           int64
	StateRevision                      string
	ExpressionRevision                 string
}
type VoiceChange struct {
	Quote     string `json:"quote"`
	UserVoice string `json:"user_voice"`
}
type VoiceUndo struct {
	Quote    string `json:"quote"`
	Revision int64  `json:"revision"`
}
type PersonaUndo struct {
	Revision int64 `json:"revision"`
}
type PersonaReceipt struct {
	Revision int64        `json:"revision"`
	Notice   string       `json:"notice,omitempty"`
	Undo     *PersonaUndo `json:"undo,omitempty"`
}

// Persona is Core's Moirai persona write path. Only canonical owner input in
// this conversation can support a change; provider text supplies no identity.
type Persona struct {
	memory        *Memory
	agents        *agents.Store
	events        *ledger.Store
	now           func() time.Time
	emotionPolicy agents.EmotionPolicy
}

func NewPersona(db *canon.Store, store *agents.Store, materials ledger.Materials) *Persona {
	return &Persona{memory: NewMemory(db, materials, nil), agents: store, events: ledger.New(db, materials), now: time.Now, emotionPolicy: agents.DefaultEmotionPolicy()}
}

func (p *Persona) confirm(ctx context.Context, scope PersonaScope, quote string) error {
	_, err := p.confirmed(ctx, scope, quote)
	return err
}
func (p *Persona) confirmed(ctx context.Context, scope PersonaScope, quote string) (ledger.Event, error) {
	if p == nil || p.agents == nil || p.events == nil || scope.Agent == "" || scope.Person == "" || scope.Conversation == "" || scope.Input == "" || scope.Revision <= 0 || strings.TrimSpace(quote) == "" || len(quote) > 8192 {
		return ledger.Event{}, ErrPersonaEvidence
	}
	event, err := p.events.Get(ctx, scope.Input)
	if err != nil {
		return ledger.Event{}, err
	}
	if event.Origin.Kind != "native" || event.Origin.Entry != guard.EntryOwner || event.SpeakerKind != guard.Person || event.PersonID != scope.Person || event.AgentID != scope.Agent || event.ConversationID != scope.Conversation || event.ReplayOf != "" || event.ScaffoldKind != "" || event.BytesUnavailable || !strings.Contains(event.Body.DerivedText(), quote) {
		return ledger.Event{}, ErrPersonaEvidence
	}
	return event, nil
}

func (p *Persona) Change(ctx context.Context, scope PersonaScope, in VoiceChange) (PersonaReceipt, error) {
	event, err := p.confirmed(ctx, scope, in.Quote)
	if err != nil {
		return PersonaReceipt{}, err
	}
	masked, err := secrets.Mask(in.UserVoice)
	if err != nil || masked.Text() != in.UserVoice || !utf8.ValidString(in.UserVoice) || strings.TrimSpace(in.UserVoice) == "" || len(in.UserVoice) > 32768 {
		return PersonaReceipt{}, agents.ErrInvalid
	}
	a, err := p.agents.ChangeVoice(ctx, scope.Agent, scope.Revision, in.UserVoice, scope.Person)
	if err != nil {
		return PersonaReceipt{}, err
	}
	r := PersonaReceipt{Revision: a.Revision}
	if a.Revision != scope.Revision {
		r.Notice = design.Text(design.ConversationLanguage(event.Body.Text(), design.Language(event.Surface.Language)), "notice.persona_changed")
		r.Undo = &PersonaUndo{Revision: a.Revision}
	}
	return r, nil
}

func (p *Persona) Undo(ctx context.Context, scope PersonaScope, in VoiceUndo) (PersonaReceipt, error) {
	event, err := p.confirmed(ctx, scope, in.Quote)
	if err != nil {
		return PersonaReceipt{}, err
	}
	if in.Revision != scope.Revision {
		return PersonaReceipt{}, agents.ErrConflict
	}
	a, err := p.agents.UndoVoice(ctx, scope.Agent, in.Revision, scope.Person)
	if err != nil {
		return PersonaReceipt{}, err
	}
	return PersonaReceipt{Revision: a.Revision, Notice: design.Text(design.ConversationLanguage(event.Body.Text(), design.Language(event.Surface.Language)), "notice.persona_undone")}, nil
}

func personaInput(raw json.RawMessage, name, target string) (tools.Input, error) {
	var value any
	switch name {
	case "change_persona":
		var in VoiceChange
		if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Quote) == "" || strings.TrimSpace(in.UserVoice) == "" || len(in.UserVoice) > 32768 {
			return tools.Input{}, tools.ErrInvalid
		}
		value = in
	case "undo_persona":
		var in VoiceUndo
		if err := decodeSkill(raw, &in); err != nil || strings.TrimSpace(in.Quote) == "" || in.Revision <= 1 {
			return tools.Input{}, tools.ErrInvalid
		}
		value = in
	default:
		return tools.Input{}, tools.ErrInvalid
	}
	args, err := json.Marshal(value)
	return tools.Input{Arguments: args, Target: target, What: "Change speaking style", Where: "This agent", ScopeKey: "persona", Write: true, Undoable: true}, err
}

// RegisterPersona binds each tool to one prepared owner turn. The shared
// registry and authority service own effect claims, replay and receipts.
func RegisterPersona(registry *tools.Registry, p *Persona, scope PersonaScope) ([]tools.Spec, error) {
	if registry == nil || p == nil || scope.Agent == "" || scope.Revision <= 0 {
		return nil, tools.ErrInvalid
	}
	var specs []tools.Spec
	for _, name := range []string{"change_persona", "undo_persona"} {
		spec := tools.Spec{Classify: tools.Class(tools.PrivateDraft), Name: name, Placement: tools.Core, Conc: 1, Deadline: 10 * time.Second, Schema: func(raw json.RawMessage) (tools.Input, error) { return personaInput(raw, name, "persona:"+scope.Agent) }}
		handler := func(ctx context.Context, _ lease.Lease, input tools.Input) (tools.Result, error) {
			var receipt PersonaReceipt
			var err error
			if name == "change_persona" {
				var in VoiceChange
				err = decodeSkill(input.Arguments, &in)
				if err == nil {
					receipt, err = p.Change(ctx, scope, in)
				}
			} else {
				var in VoiceUndo
				err = decodeSkill(input.Arguments, &in)
				if err == nil {
					receipt, err = p.Undo(ctx, scope, in)
				}
			}
			if err != nil {
				code := "persona_operation_failed"
				for _, known := range []error{ErrPersonaEvidence, agents.ErrInvalid, agents.ErrConflict, agents.ErrNotFound} {
					if errors.Is(err, known) {
						code = known.Error()
						break
					}
				}
				body, _ := json.Marshal(struct {
					Code string `json:"code"`
				}{code})
				return tools.Result{State: tools.Failed, Body: body}, nil
			}
			body, err := json.Marshal(receipt)
			if err != nil {
				return tools.Result{State: tools.Failed}, err
			}
			return tools.Result{State: tools.Applied, Body: body, Revision: strconv.FormatInt(receipt.Revision, 10)}, nil
		}
		if err := registry.Register(spec, handler); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func PersonaDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"change_persona","description":"Apply an explicit request from the person to change this agent's speaking style. Quote the current input and preserve unrelated user voice preferences. Changes apply next turn; reply length belongs in reply budgets.","parameters":{"type":"object","properties":{"quote":{"type":"string"},"user_voice":{"type":"string"}},"required":["quote","user_voice"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"undo_persona","description":"Undo the identified speaking-style revision after the person asks. Quote the current input. Restores the previous voice from the next turn.","parameters":{"type":"object","properties":{"quote":{"type":"string"},"revision":{"type":"integer","minimum":2}},"required":["quote","revision"],"additionalProperties":false}}`),
	}
}
