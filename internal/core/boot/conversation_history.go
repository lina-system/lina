// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

// Match the durable display sources, not model context. Cards replace their
// source key, and only committed stopped/failed outcomes restore partials.
// The fallback fields preserve the pre-position history order for old records
// and for durable outcomes committed before their first publication.
const conversationHistorySources = `
 SELECT e.conversation_id,CASE WHEN e.scaffold_kind='result' AND e.speaker_kind='agent' AND json_valid(e.body)
        THEN 'result-card:'||json_extract(e.body,'$.result_id') ELSE e.id END AS item_key,
        e.id AS event_id,'' AS partial,'' AS turn_id,
        e.rowid AS fallback,1 AS phase,0 AS partial_position
 FROM conv_event e
 WHERE (e.speaker_kind IN ('person','agent','forwarded') OR e.scaffold_kind IN ('interruption','failure','skill_change','persona_change','worker_session','run_handoff','background_notice','worker_result','coding_handoff','timer','plan_rejection','question_default','question_answer'))
   AND (coalesce(e.scaffold_kind,'')<>'result' OR (e.speaker_kind='agent' AND
        CASE WHEN json_valid(e.body) THEN json_type(e.body,'$.body')='object' ELSE 0 END
        AND EXISTS(SELECT 1 FROM result_heads h WHERE h.event_id=e.id)
        AND NOT EXISTS(SELECT 1 FROM plan_drafts p JOIN result_heads h ON h.id=p.result_id WHERE h.event_id=e.id)))

 UNION ALL
 SELECT e.conversation_id,h.id,e.id,'','',e.rowid,1,0
 FROM result_heads h JOIN plan_drafts p ON p.result_id=h.id JOIN conv_event e ON e.id=h.event_id
 UNION ALL
 SELECT e.conversation_id,printf('%s:%d:%d',r.id,p.round+1,p.ordinal),'',p.body,r.id,
        e.rowid,0,p.rowid
 FROM turn_runs r
 JOIN turn_control c ON c.turn_id=r.id
 JOIN conv_event e ON e.id=c.notice_event
 JOIN turn_partials p ON p.turn_id=r.id
 WHERE r.state IN ('aborted','failed')
`

type conversationHistoryItem struct {
	key, eventID, partial, turnID string
}

func (r *conversationRuntime) historyItems(ctx context.Context) ([]conversationHistoryItem, error) {
	// Bootstrap only missing positions. Never infer an old stream order from
	// timestamps or move an already positioned key when its final item arrives.
	err := r.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO conversation_order(conversation_id,item_key)
 SELECT conversation_id,item_key FROM (`+conversationHistorySources+`)
 WHERE conversation_id=? ORDER BY fallback,phase,partial_position
 ON CONFLICT DO NOTHING`, r.conversation)
		return err
	})
	if err != nil {
		return nil, err
	}
	var items []conversationHistoryItem
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.item_key,s.event_id,s.partial,s.turn_id
 FROM (`+conversationHistorySources+`) s
 JOIN conversation_order o ON o.conversation_id=s.conversation_id AND o.item_key=s.item_key
 WHERE s.conversation_id=? ORDER BY o.position DESC LIMIT 200`, r.conversation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item conversationHistoryItem
			if err := rows.Scan(&item.key, &item.eventID, &item.partial, &item.turnID); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	slices.Reverse(items)
	return items, err
}

func (r *conversationRuntime) seedHistory(ctx context.Context) error {
	if r.plans != nil {
		if err := r.publishPlanRejections(ctx); err != nil {
			return err
		}
	}
	items, err := r.historyItems(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.eventID == "" {
			masked, err := secrets.Mask(item.partial)
			if err != nil {
				return err
			}
			if err = r.publishPartial(ctx, item.key, item.turnID, masked.Text()); err != nil {
				return err
			}
			continue
		}
		e, err := r.journal().Get(ctx, item.eventID)
		if err != nil {
			return err
		}
		text := e.Body.Text()
		if e.ScaffoldKind == "result" {
			var value result.Result
			if err := json.Unmarshal([]byte(text), &value); err != nil {
				return err
			}
			var err error
			if value.Type == result.PlanType {
				err = r.publish(ctx, protocol.KindResult, value.ID, planWire(value))
			} else if e.SpeakerKind == guard.Agent {
				err = r.publishTypedResult(ctx, e)
			}
			if err != nil {
				return err
			}
			continue
		}
		if e.ScaffoldKind == "background_notice" {
			var notices []lane.Notice
			if err := json.Unmarshal([]byte(text), &notices); err != nil {
				return err
			}
			if err := r.publishNoticeText(ctx, e.ID, notices); err != nil {
				return err
			}
			continue
		}
		if e.SpeakerKind == guard.Agent {
			filter, err := conversationFilter()
			if err != nil {
				return err
			}
			view, err := filter.Push(text)
			if err != nil {
				return err
			}
			if err = r.publishReply(ctx, e.ID, "turn:"+e.ParentID, view, eventLanguage(e)); err != nil {
				return err
			}
			continue
		}
		speaker := protocol.ConversationSpeaker(e.SpeakerKind)
		p := protocol.ItemDone{Text: &text, Speaker: &speaker}
		if e.SpeakerKind == guard.Person {
			key := e.Origin.Locator
			p.EchoKey = &key
			p.PersonID = &r.person
		}
		if err = r.publish(ctx, protocol.KindItemDone, e.ID, p); err != nil {
			return err
		}
	}
	return r.setActive(ctx, false)
}
