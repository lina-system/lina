// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Card is a display projection of a Core decision, never an authority record.
// ID, epoch and revision are opaque routing data and are not display fields.
type Card struct {
	Topic                          string
	Token                          string
	pending                        *CardAnswerRequest
	ID, Epoch                      string
	Revision                       uint64
	Kind                           protocol.Kind
	Agent, Recommendation, Summary string
	Targets                        []CardField
	Choices                        []protocol.ApprovalChoice
	Form                           *protocol.QuestionForm
	Outcome                        CardOutcome
}
type CardField struct{ Name, Value string }
type CardOutcome string

const (
	CardResolved        CardOutcome = "resolved"
	CardAlreadyResolved CardOutcome = "already_resolved"
	CardWithdrawn       CardOutcome = "withdrawn"
	CardExpired         CardOutcome = "expired"
	CardStaleEpoch      CardOutcome = "stale_epoch"
	CardStaleGroup      CardOutcome = "stale_group"
	CardRefused         CardOutcome = "refused"
	CardStopped         CardOutcome = "stopped"
)

type CardView struct {
	Fields                  map[string]string
	ID                      string
	Choice                  protocol.ApprovalChoice
	TargetsVisible, Pending bool
}
type PutCard struct {
	Card   Card
	Notify bool
}
type OpenCard struct{ ID string }
type CloseCard struct{}
type ChooseApproval struct{ Choice protocol.ApprovalChoice }
type CardTargetsVisible struct{ Complete bool }
type ConfirmCard struct{}
type CardAnswerRequest struct {
	Sequence       uint64
	Token          string
	QuestionAction protocol.QuestionAnswerAction
	Values         protocol.FormValues
	ID, Epoch      string
	Revision       uint64
	Choice         protocol.ApprovalChoice
}
type CardAnswerResult struct {
	Request CardAnswerRequest
	Outcome CardOutcome
}
type CardBell struct{ Agent, Summary string }

func (PutCard) isMsg()            {}
func (OpenCard) isMsg()           {}
func (CloseCard) isMsg()          {}
func (ChooseApproval) isMsg()     {}
func (CardTargetsVisible) isMsg() {}
func (ConfirmCard) isMsg()        {}
func (CardAnswerRequest) isMsg()  {}
func (CardAnswerResult) isMsg()   {}
func (CardBell) isMsg()           {}

func (s State) cardIndex(id string) int {
	return slices.IndexFunc(s.Cards, func(c Card) bool { return c.ID == id })
}
func (s State) CurrentCard() (Card, bool) {
	at := s.cardIndex(s.CardView.ID)
	if at < 0 {
		return Card{}, false
	}
	return s.Cards[at], true
}
func cleanCard(c Card) Card {
	c.Agent = sanitize.Display(c.Agent)
	c.Recommendation = sanitize.Display(c.Recommendation)
	c.Summary = sanitize.Display(c.Summary)
	c.Targets = slices.Clone(c.Targets)
	for i := range c.Targets {
		c.Targets[i].Name = sanitize.Display(c.Targets[i].Name)
		c.Targets[i].Value = sanitize.Display(c.Targets[i].Value)
	}
	c.Choices = slices.Clone(c.Choices)
	if c.Form != nil {
		form := *c.Form
		form.PluginName = sanitize.Display(form.PluginName)
		form.Fields = slices.Clone(form.Fields)
		for i := range form.Fields {
			if form.Fields[i].Choices != nil {
				values := slices.Clone(*form.Fields[i].Choices)
				form.Fields[i].Choices = &values
			}
		}
		c.Form = &form
	}
	c.pending = nil
	return c
}
func (s State) reduceCard(msg Msg) (State, []Cmd) {
	switch m := msg.(type) {
	case ResetCardRequests:
		for i := range s.Cards {
			s.Cards[i].pending = nil
		}
		s.CardView.Pending = false
		s.CardView.Choice = ""
		s.CardView.TargetsVisible = false
	case PutCard:
		c := m.Card
		if c.ID == "" || c.Epoch != s.Epoch || c.Revision == 0 || (c.Kind != protocol.KindApprovalRequest && c.Kind != protocol.KindQuestion) {
			return s, nil
		}
		at := s.cardIndex(c.ID)
		if at >= 0 && s.Cards[at].Epoch == c.Epoch && s.Cards[at].Revision >= c.Revision {
			return s, nil
		}
		c = cleanCard(c)
		// A terminal publication may arrive before this client's correlated reply.
		// Preserve only that pending identity, never a selection or changed group.
		if at >= 0 && c.Outcome == CardResolved && s.Cards[at].Token == c.Token && s.Cards[at].Epoch == c.Epoch {
			c.pending = s.Cards[at].pending
		}
		if at < 0 {
			s.Cards = append(s.Cards, c)
		} else {
			s.Cards[at] = c
		}
		if s.CardView.ID == c.ID {
			s.CardView = CardView{ID: c.ID}
		}
		if at < 0 && m.Notify && c.Kind == protocol.KindApprovalRequest && c.Outcome == "" {
			return s, []Cmd{func(context.Context) Msg { return CardBell{Agent: c.Agent, Summary: c.Summary} }}
		}
	case OpenCard:
		if at := s.cardIndex(m.ID); at >= 0 {
			s.CardView = CardView{ID: m.ID, Pending: s.Cards[at].pending != nil}
		}
	case CloseCard:
		s.CardView = CardView{}
	case CardTargetsVisible:
		s.CardView.TargetsVisible = m.Complete
	case ChooseApproval:
		c, ok := s.CurrentCard()
		if ok && c.Kind == protocol.KindApprovalRequest && c.Outcome == "" && c.pending == nil && slices.Contains(c.Choices, m.Choice) {
			s.CardView.Choice = m.Choice
		}
	case EditQuestionField:
		c, ok := s.CurrentCard()
		if !ok || c.Kind != protocol.KindQuestion || c.Form == nil || c.Outcome != "" || c.pending != nil {
			return s, nil
		}
		if !slices.ContainsFunc(c.Form.Fields, func(f protocol.FormField) bool { return f.Name == m.Name }) {
			return s, nil
		}
		s.CardView.Fields = maps.Clone(s.CardView.Fields)
		if s.CardView.Fields == nil {
			s.CardView.Fields = map[string]string{}
		}
		s.CardView.Fields[m.Name] = m.Value
	case AnswerQuestion:
		c, ok := s.CurrentCard()
		if !ok || c.Kind != protocol.KindQuestion || c.Epoch != s.Epoch || c.Outcome != "" || c.pending != nil {
			return s, nil
		}
		var values protocol.FormValues
		switch m.Action {
		case protocol.QuestionAnswerActionAccept:
			var valid bool
			values, valid = QuestionValues(c.Form, s.CardView.Fields)
			if !valid {
				return s, nil
			}
		case protocol.QuestionAnswerActionDecline, protocol.QuestionAnswerActionCancel:
		default:
			return s, nil
		}
		s.CardSequence++
		req := CardAnswerRequest{ID: c.ID, Epoch: c.Epoch, Revision: c.Revision, Token: c.Token, Sequence: s.CardSequence, QuestionAction: m.Action, Values: values}
		c.pending = &req
		s.Cards[s.cardIndex(c.ID)] = c
		s.CardView.Pending = true
		return s, []Cmd{func(context.Context) Msg { return req }}
	case ConfirmCard:
		c, ok := s.CurrentCard()
		if !ok || c.Kind != protocol.KindApprovalRequest || c.Epoch != s.Epoch || c.Outcome != "" || c.pending != nil || !s.CardView.TargetsVisible || !slices.Contains(c.Choices, s.CardView.Choice) {
			return s, nil
		}
		s.CardSequence++
		req := CardAnswerRequest{ID: c.ID, Epoch: c.Epoch, Revision: c.Revision, Token: c.Token, Sequence: s.CardSequence, Choice: s.CardView.Choice}
		c.pending = &req
		s.Cards[s.cardIndex(c.ID)] = c
		s.CardView.Pending = true
		return s, []Cmd{func(context.Context) Msg { return req }}
	case CardAnswerResult:
		at := s.cardIndex(m.Request.ID)
		if at < 0 || m.Request.Epoch != s.Epoch || s.Cards[at].pending == nil || s.Cards[at].pending.Sequence != m.Request.Sequence || s.Cards[at].pending.Revision != m.Request.Revision || s.Cards[at].Token != m.Request.Token {
			return s, nil
		}
		s.Cards[at].Outcome = m.Outcome
		s.Cards[at].pending = nil
		if s.CardView.ID == m.Request.ID {
			s.CardView = CardView{ID: m.Request.ID}
		}
	}
	return s, nil
}

type EditQuestionField struct{ Name, Value string }
type AnswerQuestion struct{ Action protocol.QuestionAnswerAction }

func (EditQuestionField) isMsg() {}
func (AnswerQuestion) isMsg()    {}

// QuestionValues keeps numbers exact and admits only the flat Core schema.
func QuestionValues(form *protocol.QuestionForm, fields map[string]string) (protocol.FormValues, bool) {
	if form == nil {
		return nil, false
	}
	values := protocol.FormValues{}
	for _, field := range form.Fields {
		text, exists := fields[field.Name]
		if !exists {
			return nil, false
		}
		value := protocol.FormValue{}
		switch field.Type {
		case protocol.FormFieldTypeText:
			value.String = &text
		case protocol.FormFieldTypeNumber:
			if !json.Valid([]byte(text)) || text == "" {
				return nil, false
			}
			decoder := json.NewDecoder(strings.NewReader(text))
			decoder.UseNumber()
			var decoded any
			if decoder.Decode(&decoded) != nil {
				return nil, false
			}
			n, ok := decoded.(json.Number)
			if !ok {
				return nil, false
			}
			value.Number = &n
		case protocol.FormFieldTypeBoolean:
			if text != "true" && text != "false" {
				return nil, false
			}
			b, _ := strconv.ParseBool(text)
			value.Boolean = &b
		case protocol.FormFieldTypeChoice:
			if field.Choices == nil || !slices.Contains(*field.Choices, text) {
				return nil, false
			}
			value.String = &text
		default:
			return nil, false
		}
		values[field.Name] = value
	}
	return values, true
}

type ResetCardRequests struct{}

func (ResetCardRequests) isMsg() {}
