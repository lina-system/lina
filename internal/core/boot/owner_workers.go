// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
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
		if err != nil || c.Run == nil || c.Task.PersonID != "" && c.Task.PersonID != s.PersonID {
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
	if entry == nil || entry.origin.person != s.PersonID || conversation != nil && entry.origin.conversation != *conversation {
		return outsideManagement()
	}
	if card != nil && (card.Task.AgentID != entry.origin.agent || card.Task.ConversationID != entry.origin.conversation) {
		return outsideManagement()
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
	result, err := entry.origin.workerOperation(ctx, name, "", ledger.Event{}, in)
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
