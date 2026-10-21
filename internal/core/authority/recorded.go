// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

// EffectKey uses the immutable scope/call tuple shared by admission and replay.
func EffectKey(scope, action, target, intent string) string {
	raw, _ := json.Marshal([4]string{scope, action, target, intent})
	return digest(raw)
}

// RecordedCall reads the existing receipt without opening authority recovery or
// dispatch. A later approved receipt can settle an earlier awaiting-person turn
// result. The exact effect key, actor and call identity must all agree.
func RecordedCall(ctx context.Context, tx *canon.Tx, scope, person, agent, name, callID string) (Effect, bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record,running FROM authority_effects WHERE json_extract(record,'$.Person')=? AND json_extract(record,'$.Agent')=? AND json_extract(record,'$.Tool')=?", person, agent, name)
	if err != nil {
		return Effect{}, false, err
	}
	defer rows.Close()
	var found Effect
	matched := false
	for rows.Next() {
		var raw []byte
		var running bool
		var e Effect
		if err = rows.Scan(&raw, &running); err != nil {
			return Effect{}, false, err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return Effect{}, false, err
		}
		if e.Key != EffectKey(scope, name, e.Target, callID) {
			continue
		}
		if matched {
			return Effect{}, false, errors.New("ambiguous_recorded_effect")
		}
		found = e
		matched = true
		if running {
			found.Result.Body = nil
			found.State = tools.Unknown
		}
	}
	return found, matched, rows.Err()
}
