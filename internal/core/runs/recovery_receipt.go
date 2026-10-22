// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// RecoverStartReceipt confirms only an existing admission by its trusted
// origin/native call identity. It never executes StartTool or creates a run.
func RecoverStartReceipt(ctx context.Context, tx *canon.Tx, turn, conversation string, call provider.ToolCall) (*loop.Result, error) {
	if call.Name != StartToolName || call.Custom {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT record FROM background_runs WHERE json_extract(record,'$.Origin.Turn')=? AND json_extract(record,'$.Origin.Conversation')=?`, turn, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		var r Record
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		if r.StartKey == "" || r.StartKey != startIdentity(r.Origin, call.ID) {
			continue
		}
		output, err := json.Marshal(Receipt{ID: r.ID, State: r.StartState})
		if err != nil {
			return nil, err
		}
		return &loop.Result{ID: call.ID, Code: loop.OK, Output: output}, nil
	}
	return nil, rows.Err()
}
