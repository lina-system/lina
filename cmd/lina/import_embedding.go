// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/recall"
)

// The import command calls this only after reporting the volume and accepting
// --embed. Only conversations touched by this batch receive consent, bounded
// to their current event watermark so a future import is not preauthorized.
func consentImportBatch(ctx context.Context, db *canon.Store, batch string) error {
	var conversations []string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM import_conversation WHERE batch_id=?
 UNION SELECT e.conversation_id FROM messenger_legacy_ref l JOIN messenger_record_revision r
 ON r.platform=l.platform AND r.account=l.account AND r.chat=l.chat AND r.message=l.message
 JOIN conv_event e ON e.id=r.event WHERE l.batch_id=?`, batch, batch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			conversations = append(conversations, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, id := range conversations {
		if err := recall.SetImportConsent(ctx, db, id, true); err != nil {
			return err
		}
	}
	return nil
}
