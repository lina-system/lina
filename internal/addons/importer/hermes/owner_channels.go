// SPDX-License-Identifier: Apache-2.0
package hermes

import "context"

// OwnerChannelCandidate is observed host provenance, never an ownership claim.
// Include shared and delegated interactive sessions so the person can inspect
// the source; classification still enforces their non-person provenance.
type OwnerChannelCandidate struct {
	OwnerChannel
	ChatType string
}

// DiscoverOwnerChannels reads only provenance from the registered snapshot.
// LCM alone has no sender fields and cannot supply a confirmation candidate.
func DiscoverOwnerChannels(ctx context.Context, statePath string) ([]OwnerChannelCandidate, error) {
	db, err := openSpeakerSource(ctx, statePath, HermesState)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT source,id,COALESCE(user_id,''),COALESCE(chat_id,''),COALESCE(chat_type,'') FROM sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OwnerChannelCandidate{}
	for rows.Next() {
		var c OwnerChannelCandidate
		if err := rows.Scan(&c.Source, &c.SessionID, &c.UserID, &c.ChatID, &c.ChatType); err != nil {
			return nil, err
		}
		if interactiveSource(c.Source) {
			result = append(result, c)
		}
	}
	return result, rows.Err()
}
