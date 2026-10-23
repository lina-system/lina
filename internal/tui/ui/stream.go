// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func compareStreamCursor(a, b protocol.StreamCursor) int {
	aEpoch, _ := strconv.ParseUint(a.Epoch, 10, 64)
	bEpoch, _ := strconv.ParseUint(b.Epoch, 10, 64)
	if aEpoch < bEpoch {
		return -1
	}
	if aEpoch > bEpoch {
		return 1
	}
	aSeq, _ := streamNumber(a.Seq)
	bSeq, _ := streamNumber(b.Seq)
	if aSeq < bSeq {
		return -1
	}
	if aSeq > bSeq {
		return 1
	}
	return 0
}

func (m *shellModel) hasGap() bool {
	for _, topic := range m.content.Topics {
		if topic.OutOfSync {
			return true
		}
	}
	return false
}

func (m *shellModel) subscribeSnapshot(topic string) tea.Cmd {
	request := shellSubscription{Generation: m.generation, Request: protocol.Subscribe{Topic: &topic}}
	return func() tea.Msg { return request }
}

type resyncSignal struct {
	topic  string
	closed bool
}

func (m *shellModel) markResync(p streamProjection) tea.Cmd {
	if m.content.Epoch != "" {
		cursor := protocol.StreamCursor{Epoch: m.content.Epoch, Seq: json.Number(strconv.FormatUint(m.content.Topics[p.topic].Cursor, 10))}
		if compareStreamCursor(p.cursor, cursor) < 0 {
			return nil
		}
	}
	signal := resyncSignal{topic: p.topic, closed: p.request}
	if seen, ok := m.resyncSeen[signal]; ok && compareStreamCursor(p.cursor, seen) <= 0 {
		return nil
	}
	m.resyncSeen[signal] = p.cursor
	if floor, exists := m.resyncPending[p.topic]; !exists || compareStreamCursor(p.cursor, floor) > 0 {
		m.resyncPending[p.topic] = p.cursor
	}
	m.status = ConnectionStatus{State: Resyncing}
	if p.request {
		return m.subscribeSnapshot(p.topic)
	}
	return nil // Core's resync already promises a snapshot on this subscription.
}

var errStream = errors.New("invalid stream projection")

type streamProjection struct {
	message                   state.Msg
	topic                     string
	cursor                    protocol.StreamCursor
	snapshot, resync, request bool
}

// projectStream consumes only generated payloads. It converts the wire
// view into the existing pure reducer's messages; it defines no wire fields.
func projectStream(envelope protocol.Envelope, languages ...design.Language) (streamProjection, error) {
	var language design.Language
	if len(languages) > 0 {
		language = languages[0]
	}
	p := streamProjection{}
	switch payload := envelope.Payload.(type) {
	case protocol.Snapshot:
		seq, err := streamNumber(payload.Cursor.Seq)
		if err != nil || payload.Cursor.Epoch != envelope.Epoch {
			return p, errStream
		}
		items := make([]state.Block, 0, len(payload.Items))
		for _, item := range payload.Items {
			revision, err := streamNumber(item.Revision)
			if err != nil {
				return p, err
			}
			body, err := streamItemPayload(item)
			if err != nil {
				return p, err
			}
			if body == nil || item.Kind == protocol.KindToolActivity {
				continue // Optional activity consumes the snapshot cursor, never history.
			}
			_, text := streamFields(body)
			text = progressText(body, language, text)
			block := state.Block{ID: streamKey(payload.Topic, item.Key), Kind: item.Kind, Revision: revision, Done: item.Kind != protocol.KindOutputDelta}
			block.Card = projectCard(body, envelope.Epoch, revision)
			block.Waiting = projectWaiting(body)
			block.Work = projectWork(body)
			if text != nil {
				block.Text = *text
			}
			projectConversationBlock(&block, body)
			items = append(items, block)
		}
		sent, _ := time.Parse(time.RFC3339Nano, envelope.SentAt)
		p = streamProjection{topic: payload.Topic, cursor: payload.Cursor, snapshot: true, message: state.Snapshot{Epoch: payload.Cursor.Epoch, Revocation: payload.RevocationEpoch, Topic: payload.Topic, Cursor: seq, Items: items, ServerTime: sent}}
	case protocol.Resync:
		p = streamProjection{topic: payload.Topic, cursor: payload.Cursor, resync: true}
	case protocol.ResyncRequired:
		p = streamProjection{topic: payload.Topic, cursor: payload.Cursor, resync: true, request: true}
	default:
		metadata, text := streamFields(envelope.Payload)
		text = progressText(envelope.Payload, language, text)
		if metadata == nil {
			return p, nil
		}
		seq, err := streamNumber(metadata.Cursor.Seq)
		if err != nil {
			return p, err
		}
		revision, err := streamNumber(metadata.Revision)
		if err != nil {
			return p, err
		}
		value := ""
		if text != nil {
			value = *text
		}
		p.topic, p.cursor = metadata.Topic, metadata.Cursor
		if envelope.Kind == protocol.KindToolActivity {
			activity := envelope.Payload.(protocol.ToolActivity)
			call := state.ToolCall{ID: activity.CallID, Name: activity.ToolName, Status: activity.Status}
			if activity.Outcome != nil {
				call.Outcome = *activity.Outcome
			}
			if activity.Target != nil {
				call.Target = *activity.Target
			}
			if activity.Summary != nil {
				call.Summary = *activity.Summary
			}
			p.message = state.ToolEvent{Epoch: metadata.Cursor.Epoch, Topic: metadata.Topic, Cursor: seq, Call: call}
		} else if envelope.Kind == protocol.KindOutputDelta {
			p.message = state.Delta{Epoch: metadata.Cursor.Epoch, Topic: metadata.Topic, ID: streamKey(metadata.Topic, metadata.Key), Cursor: seq, Revision: revision, Text: value}
		} else {
			block := state.Block{ID: streamKey(metadata.Topic, metadata.Key), Kind: envelope.Kind, Revision: revision, Text: value}
			projectConversationBlock(&block, envelope.Payload)
			if block.TurnKnown {
				block.TurnAt, _ = time.Parse(time.RFC3339Nano, envelope.SentAt)
				if block.TurnActive {
					block.TurnStarted = block.TurnAt
				}
			}
			block.Card = projectCard(envelope.Payload, envelope.Epoch, revision)
			block.Waiting = projectWaiting(envelope.Payload)
			block.Work = projectWork(envelope.Payload)
			p.message = state.ItemDone{Epoch: metadata.Cursor.Epoch, Topic: metadata.Topic, Cursor: seq, Block: block}
		}
	}
	if p.cursor.Epoch != envelope.Epoch {
		return streamProjection{}, errStream
	}
	if _, err := streamNumber(p.cursor.Seq); err != nil {
		return streamProjection{}, err
	}
	return p, nil
}

// Topic keys are unique within a topic. Length-prefix the topic to avoid
// collisions between two valid topic/key pairs; this id is never displayed.
func streamKey(topic, key string) string { return strconv.Itoa(len(topic)) + ":" + topic + key }

func streamNumber(number json.Number) (uint64, error) {
	if !json.Valid([]byte(number)) {
		return 0, errStream
	}
	rational, ok := new(big.Rat).SetString(string(number))
	if !ok || !rational.IsInt() || !rational.Num().IsUint64() {
		return 0, errStream
	}
	return rational.Num().Uint64(), nil
}

func streamFields(payload protocol.Payload) (*protocol.StreamMetadata, *string) {
	switch p := payload.(type) {
	case protocol.OutputDelta:
		return p.Stream, p.Text
	case protocol.ItemDone:
		return p.Stream, p.Text
	case protocol.TurnState:
		return p.Stream, p.Text
	case protocol.ToolActivity:
		return p.Stream, nil
	case protocol.ApprovalRequest:
		return p.Stream, nil
	case protocol.Question:
		return p.Stream, nil
	case protocol.Result:
		return p.Stream, &p.Summary
	case protocol.RunningUpdate:
		return p.Stream, p.Text
	case protocol.ProgressUpdate:
		return p.Stream, p.Text
	case protocol.WaitingUpdate:
		return p.Stream, p.Text
	case protocol.AgentTalkEntry:
		return p.Stream, p.Text
	case protocol.AccountResult:
		return p.Stream, p.Text
	case protocol.ChannelState:
		return p.Stream, p.Text
	case protocol.TaskStatus:
		return p.Stream, p.Text
	case protocol.RunStatus:
		return p.Stream, p.Text
	case protocol.BackgroundNotice:
		return p.Stream, p.Text
	case protocol.Connected:
		return p.Stream, p.Text
	case protocol.Disconnected:
		return p.Stream, p.Text
	}
	return nil, nil
}

func decodeStreamItem[T protocol.Payload](data []byte) (protocol.Payload, error) {
	var p T
	err := json.Unmarshal(data, &p)
	return p, err
}

func streamItemPayload(item protocol.StreamItem) (protocol.Payload, error) {
	switch string(item.Kind) {
	case "output_delta":
		return decodeStreamItem[protocol.OutputDelta](item.Payload)
	case "item_done":
		return decodeStreamItem[protocol.ItemDone](item.Payload)
	case "turn_state":
		return decodeStreamItem[protocol.TurnState](item.Payload)
	case "tool_activity":
		return decodeStreamItem[protocol.ToolActivity](item.Payload)
	case "approval_request":
		return decodeStreamItem[protocol.ApprovalRequest](item.Payload)
	case "question":
		return decodeStreamItem[protocol.Question](item.Payload)
	case "result":
		return decodeStreamItem[protocol.Result](item.Payload)
	case "running_update":
		return decodeStreamItem[protocol.RunningUpdate](item.Payload)
	case "progress_update":
		return decodeStreamItem[protocol.ProgressUpdate](item.Payload)
	case "waiting_update":
		return decodeStreamItem[protocol.WaitingUpdate](item.Payload)
	case "agent_talk_entry":
		return decodeStreamItem[protocol.AgentTalkEntry](item.Payload)
	case "account_result":
		return decodeStreamItem[protocol.AccountResult](item.Payload)
	case "channel_state":
		return decodeStreamItem[protocol.ChannelState](item.Payload)
	case "task_status":
		return decodeStreamItem[protocol.TaskStatus](item.Payload)
	case "run_status":
		return decodeStreamItem[protocol.RunStatus](item.Payload)
	case "background_notice":
		return decodeStreamItem[protocol.BackgroundNotice](item.Payload)
	case "connected":
		return decodeStreamItem[protocol.Connected](item.Payload)
	case "disconnected":
		return decodeStreamItem[protocol.Disconnected](item.Payload)
	}
	return nil, nil // Unknown optional snapshot items have no display projection.
}

func projectConversationBlock(block *state.Block, payload protocol.Payload) {
	switch p := payload.(type) {
	case protocol.Connected:
		block.ShellFileToolsAvailable = p.ShellFileToolsAvailable
	case protocol.RunningUpdate:
		if p.State != nil && p.Summary != nil {
			block.RunState = *p.State
			block.Text = *p.Summary
			if p.Reason != nil {
				block.RunReason = *p.Reason
			}
		}
		if p.Removed != nil {
			block.Removed = *p.Removed
		}
		if p.RunID != nil {
			block.RunID = *p.RunID
		}
	case protocol.ItemDone:
		if p.EchoKey != nil {
			block.EchoKey = *p.EchoKey
		}
		if p.Speaker != nil {
			block.Speaker = *p.Speaker
		}
		if p.Details != nil && block.Speaker == protocol.ConversationSpeakerSystem {
			block.Body = *p.Details
			block.Title = block.Text
		}
	case protocol.TurnState:
		if p.Phase != nil {
			block.TurnPhase = *p.Phase
		}
		if p.ToolName != nil {
			block.ToolName = *p.ToolName
		}
		if p.Active != nil {
			block.TurnKnown = true
			block.TurnActive = *p.Active
		}
	case protocol.Result:
		block.ResultID, block.ResultRevision, block.ResultPaged = p.ResultID, string(p.Revision), p.Next != nil
		if p.RunID != nil {
			block.RunID = *p.RunID
		}
		block.Title = p.Title
		block.CompletionLabel = ""
		if p.Completion != nil {
			block.CompletionLabel = p.Completion.Label
		}
		if p.Text != nil {
			block.Body = *p.Text
			// Core's reply-shape previews reuse the source conversation key
			// and derive their result id from it. Explicit structured results
			// have their own identity, even when their title is "Full reply".
			block.InlineReply = p.Type == protocol.ResultTypeDocumentDraft && p.Status == protocol.ResultStatusPreview &&
				p.RunID == nil && strings.HasPrefix(p.ResultID, "result:") &&
				block.ID == streamKey("conversation:"+p.Provenance.ConversationID, strings.TrimPrefix(p.ResultID, "result:"))
			if block.InlineReply {
				block.Text = block.Body
				block.Speaker = protocol.ConversationSpeakerAgent
			}
		}
	}
}

func projectCard(payload protocol.Payload, epoch string, revision uint64) *state.Card {
	var wire *protocol.DecisionCard
	var card state.Card
	switch p := payload.(type) {
	case protocol.ApprovalRequest:
		wire = p.Card
		card.Kind = protocol.KindApprovalRequest
		card.Choices = p.Choices
	case protocol.Question:
		wire = p.Card
		card.Kind = protocol.KindQuestion
		card.ConversationID = p.ConversationID
		card.Form = p.Form
	default:
		return nil
	}
	if wire == nil {
		return nil
	}
	card.ID = wire.ID
	card.Epoch = epoch
	card.Revision = revision
	card.Token = wire.Revision
	card.Agent = wire.Agent
	card.Summary = wire.Summary
	if wire.Recommendation != nil {
		card.Recommendation = *wire.Recommendation
	}
	if wire.Outcome != nil {
		card.Outcome = state.CardOutcome(*wire.Outcome)
	}
	for _, field := range wire.Targets {
		card.Targets = append(card.Targets, state.CardField{Name: field.Name, Value: field.Value})
	}
	return &card
}
