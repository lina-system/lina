// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/provider"
)

// A queued dispatch can start only after its successful result commits to the
// originating turn. A bridge changes the journaled name, not that requirement
// or the call identity from which the dispatch receipt was derived.
func (s *Service) admissionCommitted(ctx context.Context, r Record) (bool, error) {
	committed := false
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT call FROM turn_calls WHERE turn_id=? AND json_extract(result,'$.code')='ok' AND json_extract(result,'$.output.id')=?`, r.Origin.Turn, r.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			var call provider.ToolCall
			if json.Unmarshal(raw, &call) != nil || call.Custom || call.ID == "" || admissionID(r.Origin, call.ID) != r.ID {
				continue
			}
			name := call.Name
			if name == "call_tool" {
				var nested struct {
					Name string `json:"name"`
				}
				if json.Unmarshal([]byte(call.Arguments), &nested) != nil {
					continue
				}
				name = nested.Name
			}
			if name == "dispatch_work" {
				committed = true
				return nil
			}
		}
		return rows.Err()
	})
	return committed, err
}
