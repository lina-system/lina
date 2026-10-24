// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

func workerReference(id string) bool {
	return strings.HasPrefix(id, "session:") || strings.HasPrefix(id, "task:")
}

// Resolve task generations and live sessions through their owners. The existing
// worker operation rechecks the observed handle under its control lock before
// delivering input or an interrupt. A request is never a completion verdict.
func (r *conversationRuntime) ownerWorkerControl(ctx context.Context, s clientsrv.Session, id string, conversation *string, key, note string) error {
	return r.ownerWorkerControlFrom(ctx, s, id, conversation, key, note, nil)
}
func (r *conversationRuntime) ownerWorkerControlFrom(ctx context.Context, s clientsrv.Session, id string, conversation *string, key, note string, source json.RawMessage) error {
	if err := r.authorize(s, nil); err != nil {
		return err
	}
	if conversation != nil && *conversation == "" {
		return malformedManagement()
	}
	session := strings.TrimPrefix(id, "session:")
	var card *work.Card
	if ref, ok := strings.CutPrefix(id, "task:"); ok {
		var c work.Card
		err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; c, err = work.ReadReference(ctx, tx, ref); return err })
		if err != nil || c.Task.PersonID != "" && c.Task.PersonID != s.PersonID {
			return outsideManagement()
		}
		if c.Task.Schedule != nil && note == "" {
			if _, err := r.stopScheduled(ctx, s.PersonID, conversation, ref); err != nil {
				return outsideManagement()
			}
			if c.Run == nil || !c.Active {
				r.notifyWorkerRunning()
				r.notifyOwnerViews()
				return nil
			}
		}
		if c.Run == nil {
			return outsideManagement()
		}
		card = &c
		session = c.Run.ThreadID
	}
	var entry *workerSession
	for _, e := range r.workers.entries() {
		if e.thread.ID == session {
			entry = e
			break
		}
	}
	if entry == nil && note != "" {
		return r.offlineSteering(ctx, s.PersonID, session, card, conversation, key, note, source)
	}
	if entry == nil || entry.origin.person != s.PersonID || !workerControlConversation(entry.origin, conversation) {
		return outsideManagement()
	}
	if card != nil && (card.Task.AgentID != entry.origin.agent || card.Task.ConversationID != entry.origin.conversation) {
		return outsideManagement()
	}
	if entry.scheduled && note == "" && card == nil {
		c, err := r.work.Get(ctx, entry.thread.TaskID)
		if err != nil || c.Run == nil || c.Run.ThreadID != entry.thread.ID {
			return outsideManagement()
		}
		if _, err = r.stopScheduled(ctx, s.PersonID, conversation, fmt.Sprintf("%s:%d", c.Task.ID, c.Run.Generation)); err != nil {
			return outsideManagement()
		}
	}
	entry.ownerControl.Lock()
	defer entry.ownerControl.Unlock()
	name := "worker_interrupt"
	if note != "" {
		name = "worker_input"
	}
	fingerprint := name + "\n" + note
	if key != "" && entry.ownerRequests[key] != "" {
		if entry.ownerRequests[key] != fingerprint {
			return malformedManagement()
		}
		return nil
	}
	if note != "" {
		return entry.ownerSteering(ctx, key, note, source)
	}
	snapshot, err := entry.adapter.Snapshot(ctx, entry.thread)
	if err != nil {
		return err
	}
	if card != nil && (snapshot.Thread.TaskID != card.Task.ID || snapshot.Thread.TurnID != card.Run.TurnID) {
		return outsideManagement()
	}
	args := workerArgs{Session: session, Turn: snapshot.Thread.TurnID, Generation: snapshot.Thread.ProcessGeneration, Binding: snapshot.Thread.BindingEpoch, Text: note}
	if note == "" {
		args.Quote = "stop"
	}
	raw, _ := json.Marshal(args)
	in, err := workerSchema(name, raw)
	if err != nil {
		return malformedManagement()
	}
	decision := key
	if decision == "" {
		decision = rand.Text()
	}
	result, err := entry.origin.workerOperationWithOwner(ctx, name, decision, ledger.Event{}, in, true)
	if err != nil {
		return err
	}
	if result.State != tools.Applied {
		return &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	if key != "" {
		if entry.ownerRequests == nil {
			entry.ownerRequests = map[string]string{}
		}
		entry.ownerRequests[key] = fingerprint
	}
	r.notifyOwnerViews()
	return nil
}

func workerControlConversation(origin *conversationRuntime, conversation *string) bool {
	return conversation == nil || origin.conversation == *conversation || origin.workerDelivery().conversation == *conversation
}
