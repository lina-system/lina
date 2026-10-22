// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Channel commands enter the same Core handlers as client requests. A durable
// claim precedes control, so recovery cannot repeat stop-all against new work.
func (c *channelRuntime) channelControl(ctx context.Context, shared *conversationRuntime, event gateway.Inbound, b channels.Binding, in channelInput) (bool, error) {
	var body struct {
		Text      string `json:"text"`
		Forwarded bool   `json:"forwarded"`
	}
	if json.Unmarshal(event.Body, &body) != nil {
		return false, channels.ErrInvalid
	}
	if body.Forwarded {
		return false, nil
	}
	scope := b.Conversation
	if event.Direct {
		scope = ""
	}
	kind, p, ok := protocol.OwnerCommand(body.Text, scope)
	if !ok {
		return false, nil
	}
	id := "channel-input:" + strconv.FormatInt(in.id, 10)
	now := time.Now()
	input, err := shared.journal().Get(ctx, id)
	if errors.Is(err, ledger.ErrNotFound) {
		input, err = shared.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: id, ConversationID: b.Conversation, PersonID: b.Person, AgentID: b.Agent, Surface: ledger.Surface{Kind: "channel", Platform: event.Platform, ChatID: event.Chat}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: in.native, Body: body.Text})
	}
	if err != nil {
		return true, err
	}
	claimed := false
	err = c.r.db.Write(ctx, func(tx *canon.Tx) error {
		res, e := tx.ExecContext(ctx, "INSERT INTO channel_steering(inbox_id) VALUES(?) ON CONFLICT DO NOTHING", in.id)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		claimed = n == 1
		return e
	})
	if err != nil || !claimed {
		return true, err
	}
	session := clientsrv.Session{PersonID: b.Person}
	result := clientsrv.Result{}
	// Owner-wide views must never be projected into a shared destination.
	if (kind == protocol.KindProgressDigestGet || kind == protocol.KindWaitingGet) && (!event.Direct || !b.Scope.Memory) {
		err = outsideManagement()
	} else if kind == protocol.KindFollowUp && !b.Scope.Tools {
		err = outsideManagement()
	} else if kind == protocol.KindFollowUp {
		source, _ := json.Marshal(channelFollowSource{Address: event.Address, Binding: b, Sender: event.Sender, Direct: event.Direct, Addressed: event.Addressed})
		result, err = c.r.followRunWithSource(ctx, session, protocol.Envelope{MessageID: id, Kind: kind, Payload: p}, source)
	} else if kind == protocol.KindInterrupt {
		err = shared.Interrupt(ctx, session, protocol.Interrupt{ConversationID: &b.Conversation})
	} else if op, exists := c.r.identity.Operations[kind]; exists {
		result, err = op.Handle(ctx, session, protocol.Envelope{MessageID: id, Kind: kind, Payload: p})
	} else {
		err = malformedManagement()
	}
	text := "Request applied."
	if err != nil {
		text = "This request could not be applied. Open LINA to check the work."
	} else {
		switch v := result.Payload.(type) {
		case protocol.ProgressDigest:
			lines := append([]string{}, v.Lines...)
			lines = append(lines, fmt.Sprintf("%s items · %s more", v.Total, v.More)+" · "+v.Link)
			text = strings.Join(lines, "\n")
		case protocol.Waiting:
			lines := []string{}
			if v.Rows != nil {
				for _, row := range *v.Rows {
					lines = append(lines, strings.Join(strings.Fields(row.Line.Sentence), " "))
				}
			}
			if len(lines) == 0 {
				lines = append(lines, "Nothing to decide now.")
			}
			text = strings.Join(lines, "\n")
		case protocol.MultiTaskControl:
			text = fmt.Sprintf("Stop requested for %s runs.", *v.Count)
		case protocol.FollowUp:
			text = "Note queued."
		case protocol.StopResult:
			text = "Stop requested."
		}
	}
	_, err = shared.journal().Admit(ctx, guard.EntryAgent, ledger.Input{ID: "channel-control:" + strconv.FormatInt(in.id, 10), ParentID: input.ID, ConversationID: b.Conversation, AgentID: b.Agent, Surface: input.Surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	return true, err
}

type channelFollowSource struct {
	Address           channels.Address
	Binding           channels.Binding
	Sender            string
	Direct, Addressed bool
}

func (r *conversationRuntime) authorizeRunFollowSource(ctx context.Context, record runs.Record, source json.RawMessage) error {
	var scope channelFollowSource
	if decodeWorkerArgs(source, &scope) != nil || !scope.Address.Valid() || scope.Binding.Person != record.Origin.Person || !scope.Binding.Scope.Tools {
		return runs.ErrScope
	}
	var current channels.Binding
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		current, err = channels.AdmitScope(ctx, tx, scope.Address, scope.Sender, scope.Direct, scope.Addressed)
		return err
	})
	if err != nil || current != scope.Binding || current.Unreachable {
		return runs.ErrScope
	}
	return nil
}
