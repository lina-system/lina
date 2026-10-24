// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// ComparisonRequest selects one person's source and a half-open message-time interval.
type ComparisonRequest struct {
	Person, Platform, Account string
	Since, Until              time.Time
}

// ComparisonRecord is a content-free platform key from the handed-over inbox.
type ComparisonRecord struct {
	Chat, Message string
	At            time.Time
}

// ChatComparison contains only identifiers and counts, never conversation text.
type ChatComparison struct {
	Chat            string   `json:"chat"`
	MurmurCount     int      `json:"murmur_count"`
	LinaCount       int      `json:"lina_count"`
	MissingInLina   []string `json:"missing_in_lina"`
	MissingInMurmur []string `json:"missing_in_murmur"`
}

// Comparison is collection evidence, not an assertion that cutover is ready.
type Comparison struct {
	Since       time.Time        `json:"since"`
	Until       time.Time        `json:"until"`
	MurmurCount int              `json:"murmur_count"`
	LinaCount   int              `json:"lina_count"`
	Chats       []ChatComparison `json:"chats"`
}

// Compare reads one canon snapshot; it never changes source state or records.
func (s *Store) Compare(ctx context.Context, q ComparisonRequest, inbox []ComparisonRecord) (Comparison, error) {
	if !validName(q.Person) || !validName(q.Platform) || !validName(q.Account) || q.Since.IsZero() || !q.Until.After(q.Since) {
		return Comparison{}, ErrInvalid
	}
	type sets struct{ murmur, lina map[string]bool }
	chats := map[string]*sets{}
	chat := func(id string) *sets {
		if chats[id] == nil {
			chats[id] = &sets{map[string]bool{}, map[string]bool{}}
		}
		return chats[id]
	}
	inWindow := func(at time.Time) bool { return !at.Before(q.Since) && at.Before(q.Until) }
	for _, record := range inbox {
		if err := ctx.Err(); err != nil {
			return Comparison{}, err
		}
		if !validName(record.Chat) || !validName(record.Message) || record.At.IsZero() {
			return Comparison{}, ErrInvalid
		}
		if inWindow(record.At) {
			chat(record.Chat).murmur[record.Message] = true
		}
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var person string
		if err := tx.QueryRowContext(ctx, "SELECT person FROM messenger_source WHERE platform=? AND account=? AND person=?", q.Platform, q.Account, q.Person).Scan(&person); err != nil {
			return err
		}
		// Parse times rather than comparing RFC3339 text: offsets and fractional
		// seconds must have exactly the same boundary semantics as the inbox.
		rows, err := tx.QueryContext(ctx, "SELECT chat,message,occurred_at FROM messenger_record WHERE platform=? AND account=?", q.Platform, q.Account)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, message, timestamp string
			if err := rows.Scan(&id, &message, &timestamp); err != nil {
				return err
			}
			at, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil {
				return ErrInvalid
			}
			if inWindow(at) {
				chat(id).lina[message] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return Comparison{}, err
	}
	result := Comparison{Since: q.Since.UTC(), Until: q.Until.UTC(), Chats: []ChatComparison{}}
	for id, keys := range chats {
		entry := ChatComparison{Chat: id, MurmurCount: len(keys.murmur), LinaCount: len(keys.lina), MissingInLina: []string{}, MissingInMurmur: []string{}}
		for key := range keys.murmur {
			if !keys.lina[key] {
				entry.MissingInLina = append(entry.MissingInLina, key)
			}
		}
		for key := range keys.lina {
			if !keys.murmur[key] {
				entry.MissingInMurmur = append(entry.MissingInMurmur, key)
			}
		}
		sort.Strings(entry.MissingInLina)
		sort.Strings(entry.MissingInMurmur)
		result.MurmurCount += entry.MurmurCount
		result.LinaCount += entry.LinaCount
		result.Chats = append(result.Chats, entry)
	}
	sort.Slice(result.Chats, func(i, j int) bool { return result.Chats[i].Chat < result.Chats[j].Chat })
	return result, nil
}
