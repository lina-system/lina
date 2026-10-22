// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Match the durable display sources, not model context. Cards replace their
// source key, and only committed stopped/failed outcomes restore partials.
// The fallback fields preserve the pre-position history order for old records
// and for durable outcomes committed before their first publication.
const conversationHistorySources = `
 SELECT e.conversation_id,e.id AS item_key,e.id AS event_id,'' AS partial,'' AS turn_id,
        e.rowid AS fallback,1 AS phase,0 AS partial_position
 FROM conv_event e
 WHERE (e.speaker_kind IN ('person','agent') OR e.scaffold_kind IN ('interruption','failure','skill_change','persona_change','worker_session','run_handoff','background_notice'))
   AND coalesce(e.scaffold_kind,'')<>'result'
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
