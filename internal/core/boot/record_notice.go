// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func (r *conversationRuntime) recordNotice(ctx context.Context, input ledger.Event, call provider.ToolCall, result loop.Result) loop.Result {
	if result.Code != loop.OK || call.Name != "promote_memo" && call.Name != "undo_record" && call.Name != "propose_candidate" {
		return result
	}
	var receipt records.Applied
	if json.Unmarshal(result.Output, &receipt) != nil || receipt.Notice == "" {
		return result
	}
	_ = r.publishRecordNotices(ctx)
	// The committed receipt is still successful if presentation needs recovery.
	return result
}

func (r *conversationRuntime) publishRecordNotices(ctx context.Context) error {
	if r.records == nil || r.router == nil {
		return nil
	}
	after := ""
	for {
		notices, err := r.records.Notices(ctx, r.person, after)
		if err != nil {
			return err
		}
		for _, n := range notices {
			view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, n.Agent)
			if err != nil {
				continue
			}
			text := n.Notice
			if !strings.Contains(text, n.Handle) {
				text += " Undo: " + n.Handle
			}
			entries := []struct{ id, text string }{{"record-notice:" + n.Handle, text}}
			if n.UndoRevision != "" {
				entries = append(entries, struct{ id, text string }{"record-notice:" + n.Handle + ":undo", "Change undone."})
			}
			for _, entry := range entries {
				_, err := view.journal().Get(ctx, entry.id)
				if err == nil {
					continue
				}
				if !errors.Is(err, ledger.ErrNotFound) {
					return err
				}
				now := time.Now()
				event, err := view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: entry.id, ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "record_change", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: entry.text})
				if err != nil {
					return err
				}
				text, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
				if err = view.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker}); err != nil {
					return err
				}
			}
		}
		if len(notices) < 100 {
			return nil
		}
		after = notices[len(notices)-1].Handle
	}
}
