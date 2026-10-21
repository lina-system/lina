// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/provider"
)

// A process may die after an item commits but before the stream's terminal
// event and whole call-message commit. Close those calls too, without dispatch.
func recoverItems(ctx context.Context, tx *canon.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT i.turn_id,i.round,i.item FROM turn_items i JOIN turn_runs r ON r.id=i.turn_id WHERE r.state='active' ORDER BY i.turn_id,i.round,i.ordinal`)
	if err != nil {
		return err
	}
	type item struct {
		id    string
		round int
		raw   []byte
	}
	var items []item
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.round, &v.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		var wire struct {
			Type, Name, Arguments, Input string
			ID                           string `json:"call_id"`
		}
		if json.Unmarshal(v.raw, &wire) != nil || wire.ID == "" || wire.Name == "" || (wire.Type != "function_call" && wire.Type != "custom_tool_call") {
			continue
		}
		c := provider.ToolCall{ID: wire.ID, Name: wire.Name, Arguments: wire.Arguments, Custom: wire.Type == "custom_tool_call"}
		if c.Custom {
			c.Arguments = wire.Input
		}
		b, _ := json.Marshal(c)
		_, err = tx.ExecContext(ctx, `INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call)
 SELECT ?,?,COALESCE(MAX(ordinal)+1,0),?,? FROM turn_calls WHERE turn_id=? AND round=?
 ON CONFLICT(turn_id,call_id) DO NOTHING`, v.id, v.round, c.ID, b, v.id, v.round)
		if err != nil {
			return err
		}
	}
	return nil
}
