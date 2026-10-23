// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
)

// ConformanceIn commits a Core-owned delivery decision together with its review
// record. The current receipt and observed head must still match at commit.
func ConformanceIn(ctx context.Context, tx *canon.Tx, task, receipt, head, verdict, evidence string) error {
	var raw []byte
	var card Card
	if err := tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", task).Scan(&raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &card); err != nil {
		return err
	}
	if card.Head != head || card.CompletionHead != head || card.Receipt == nil || card.Receipt.EventID != receipt || card.Active {
		return ErrState
	}
	if verdict == "unverified" {
		return nil
	}
	e := Event{Key: "judge:" + receipt, TaskID: task, Kind: "judged", ReceiptID: receipt, Value: verdict, Evidence: evidence}
	if verdict == "" {
		e.Key = "block:" + receipt
		e.Kind = "blocked"
	}
	return appendEvent(ctx, tx, e)
}
