// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	acpadapter "github.com/thisisjun786/lina/internal/core/worker/acp"
	"github.com/thisisjun786/lina/kit/protocol"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

const stoppedQuestionOutcome = protocol.DecisionOutcomeStopped

type workerQuestion struct {
	card      protocol.Question
	event     worker.Event
	requestID string
	kind      string
	requested time.Time
}
type workerQuestions struct{ runtime *conversationRuntime }

func (e *workerSession) askWorker(event worker.Event, text string) {
	e.presentQuestion(event, event.ToolCall.ID, "tool", text, []protocol.FormField{{Name: "answer", Type: protocol.FormFieldTypeText}}, nil)
}
func (e *workerSession) requestQuestion(event worker.Event) {
	req := event.Question
	switch req.Method {
	case "elicitation/create":
		form, err := acpadapter.ParseForm(req.Params)
		if err != nil {
			e.bridgeFailure(err)
			return
		}
		e.presentQuestion(event, req.ID, "acp", form.Message, form.Fields, form.Targets)
	case "item/tool/requestUserInput":
		var params wire.ToolRequestUserInputParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			e.bridgeFailure(err)
			return
		}
		fields := []protocol.FormField{}
		targets := []protocol.DecisionTarget{}
		for _, q := range params.Questions {
			if q.IsSecret != nil && *q.IsSecret {
				e.bridgeFailure(errors.New("secret worker input is unsupported"))
				return
			}
			fields = append(fields, protocol.FormField{Name: q.ID, Type: protocol.FormFieldTypeText})
			text := q.Question
			if q.Options != nil {
				for _, option := range *q.Options {
					text += "\n" + option.Label + ": " + option.Description
				}
			}
			targets = append(targets, protocol.DecisionTarget{Name: q.ID, Value: text})
		}
		e.presentQuestion(event, req.ID, "input", "Codex has a question", fields, targets)
	case "mcpServer/elicitation/request":
		// This schema baseline carries no form. Show a bounded explicit decision;
		// accepting it cannot invent field values or credentials.
		e.presentQuestion(event, req.ID, "elicitation", "Codex requests an MCP interaction", []protocol.FormField{{Name: "response", Type: protocol.FormFieldTypeChoice, Choices: &[]string{"decline", "cancel"}}}, nil)
	default:
		e.bridgeFailure(errors.New("unsupported worker question"))
	}
}
func (e *workerSession) presentQuestion(event worker.Event, requestID, kind, text string, fields []protocol.FormField, targets []protocol.DecisionTarget) {
	requested := time.Now()
	defer e.origin.notifyOwnerViews()
	id := e.requestKey(requestID)
	if len(fields) == 0 {
		e.bridgeFailure(errors.New("empty worker question"))
		return
	}
	if targets == nil {
		targets = []protocol.DecisionTarget{}
	}
	workerName := "Codex"
	if e.adapter != nil && e.adapter.Name() != "codex" {
		workerName = e.adapter.Name()
	}
	name := e.origin.router.root.decisions.AgentName(e.origin.agent)
	raw, err := json.Marshal(protocol.Question{ConversationID: e.origin.workerDelivery().conversation, Card: &protocol.DecisionCard{ID: id, Revision: "1", Agent: name, Summary: text, Targets: targets}, Form: &protocol.QuestionForm{PluginName: workerName, Fields: fields}})
	if err != nil {
		e.bridgeFailure(err)
		return
	}
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		e.bridgeFailure(err)
		return
	}
	var card protocol.Question
	if err := json.Unmarshal([]byte(masked.Text()), &card); err != nil {
		e.bridgeFailure(err)
		return
	}
	if err := e.origin.workerNotice(e.origin.ctx, e.input, id, workerName+" question: "+text); err != nil {
		e.bridgeFailure(err)
		return
	}
	e.mu.Lock()
	e.questions[id] = &workerQuestion{card: card, event: event, requestID: requestID, kind: kind, requested: requested}
	e.mu.Unlock()
	if err := e.origin.router.root.decisions.PublishQuestion(e.origin.ctx, id); err != nil {
		e.bridgeFailure(err)
	}
}
func (q workerQuestions) find(person, id string) (*workerSession, *workerQuestion, error) {
	for _, e := range q.runtime.workers.entries() {
		e.mu.Lock()
		item := e.questions[id]
		if item != nil && e.origin.person == person {
			return e, item, nil
		} // caller unlocks
		e.mu.Unlock()
	}
	return nil, nil, errors.New("worker question unavailable")
}
func (q workerQuestions) Question(ctx context.Context, person, id string) (protocol.Question, error) {
	if strings.HasPrefix(id, "a2a:") {
		return q.runtime.a2aQuestion(ctx, person, id)
	}
	if !strings.HasPrefix(id, "worker-request:") {
		return (runQuestions{runtime: q.runtime}).Question(ctx, person, id)
	}
	e, item, err := q.find(person, id)
	if err != nil {
		return protocol.Question{}, err
	}
	defer e.mu.Unlock()
	raw, err := json.Marshal(item.card)
	var card protocol.Question
	if err == nil {
		err = json.Unmarshal(raw, &card)
	}
	return card, err
}

// Preserve the work-round answer attribution added on dev while routing worker
// questions through the same existing bridge.
func (q workerQuestions) AnswerQuestionMessage(ctx context.Context, person string, answer protocol.QuestionAnswer, messageID string) (protocol.DecisionOutcome, error) {
	if answer.CardID != nil && strings.HasPrefix(*answer.CardID, conversationQuestionPrefix) {
		return q.runtime.answerConversationQuestion(ctx, person, answer, messageID)
	}
	if answer.CardID != nil && strings.HasPrefix(*answer.CardID, "work:") {
		return (runQuestions{runtime: q.runtime}).AnswerQuestionMessage(ctx, person, answer, messageID)
	}
	return q.AnswerQuestion(ctx, person, answer)
}

func (q workerQuestions) AnswerQuestion(ctx context.Context, person string, answer protocol.QuestionAnswer) (protocol.DecisionOutcome, error) {
	defer q.runtime.notifyOwnerViews()
	if answer.CardID != nil && strings.HasPrefix(*answer.CardID, "a2a:") {
		return q.runtime.answerA2AQuestion(ctx, person, answer)
	}
	if answer.CardID == nil {
		return protocol.DecisionOutcomeRefused, nil
	}
	if !strings.HasPrefix(*answer.CardID, "worker-request:") {
		return (runQuestions{runtime: q.runtime}).AnswerQuestion(ctx, person, answer)
	}
	e, item, err := q.find(person, *answer.CardID)
	if err != nil {
		return protocol.DecisionOutcomeRefused, err
	}
	if answer.Revision == nil || *answer.Revision != item.card.Card.Revision {
		e.mu.Unlock()
		return protocol.DecisionOutcomeStaleGroup, nil
	}
	if item.card.Card.Outcome != nil {
		outcome := *item.card.Card.Outcome
		e.mu.Unlock()
		return outcome, nil
	}
	if item.kind == "recovery" {
		e.mu.Unlock()
		outcome, err := e.answerRecovery(ctx, answer)
		if err == nil && outcome == protocol.DecisionOutcomeResolved {
			e.mu.Lock()
			item.card.Card.Outcome = &outcome
			e.mu.Unlock()
		}
		return outcome, err
	}
	var raw json.RawMessage
	if item.kind == "acp" {
		form, parseErr := acpadapter.ParseForm(item.event.Question.Params)
		if parseErr == nil {
			raw, parseErr = form.Reply(answer)
		}
		if parseErr != nil {
			e.mu.Unlock()
			return protocol.DecisionOutcomeRefused, nil
		}

	}
	// Claim before sending: loss of the RPC is not permission to replay an answer.
	outcome := protocol.DecisionOutcomeResolved
	item.card.Card.Outcome = &outcome
	event, requestID, kind := item.event, item.requestID, item.kind
	fields := item.card.Form.Fields
	e.mu.Unlock()
	switch kind {
	case "acp":
		// Validated before claiming the card; raw retains the typed form values.
	case "tool":
		text := ""
		if answer.Values != nil {
			if value := (*answer.Values)["answer"]; value.String != nil {
				text = *value.String
			}
		}
		raw = toolReply(map[string]any{"answer": text, "action": answer.Answer}, answer.Answer == protocol.QuestionAnswerActionAccept)
	case "input":
		response := wire.ToolRequestUserInputResponse{Answers: map[string]wire.ToolRequestUserInputAnswer{}}
		for _, field := range fields {
			values := []string{}
			if answer.Answer == protocol.QuestionAnswerActionAccept && answer.Values != nil {
				value := (*answer.Values)[field.Name]
				if value.String != nil {
					values = append(values, *value.String)
				}
			}
			response.Answers[field.Name] = wire.ToolRequestUserInputAnswer{Answers: values}
		}
		raw, err = json.Marshal(response)
	case "elicitation":
		action := wire.McpServerElicitationActionDecline
		if answer.Answer == protocol.QuestionAnswerActionCancel {
			action = wire.McpServerElicitationActionCancel
		} else if answer.Values != nil {
			value := (*answer.Values)["response"]
			if value.String != nil && *value.String == "cancel" {
				action = wire.McpServerElicitationActionCancel
			}
		}
		// No requested form is present in the pinned schema; do not grant it.
		raw, err = json.Marshal(wire.McpServerElicitationRequestResponse{Action: action})
	}
	if err == nil {
		err = e.origin.workerNotice(ctx, e.input, *answer.CardID+":answer", "Your response to the worker was recorded.")
	}
	if err == nil {
		sendCtx, cancel := context.WithTimeout(e.origin.ctx, 10*time.Second)
		err = e.adapter.Answer(sendCtx, event.Thread, worker.Answer{RequestID: requestID, Result: raw})
		cancel()
	}
	if err != nil {
		e.mu.Lock()
		stopped := protocol.DecisionOutcomeStopped
		item.card.Card.Outcome = &stopped
		e.mu.Unlock()
		e.bridgeFailure(err)
		return stopped, nil
	}
	e.answerDelivered(ctx, event.Thread)
	return outcome, nil
}
func (r *conversationRuntime) restoreWorkerQuestions(ctx context.Context) error {
	for _, e := range r.workers.entries() {
		e.mu.Lock()
		ids := make([]string, 0, len(e.questions))
		for id := range e.questions {
			ids = append(ids, id)
		}
		e.mu.Unlock()
		for _, id := range ids {
			if err := r.decisions.PublishQuestion(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}
