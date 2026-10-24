// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// ComparisonRequest scopes an ID comparison to one person's KakaoTalk account
// and the half-open message-time interval [Since, Until).
type ComparisonRequest struct {
	Person, Account string
	Since, Until    time.Time
}

// Comparison contains identifiers and counts only. A zero difference does not
// establish that either collector ran throughout the requested interval.
type Comparison struct {
	Account       string           `json:"account"`
	Since         time.Time        `json:"since"`
	Until         time.Time        `json:"until"`
	Murmur        int              `json:"murmur"`
	LINA          int              `json:"lina"`
	MissingLINA   int              `json:"missing_lina"`
	MissingMurmur int              `json:"missing_murmur"`
	Chats         []ChatComparison `json:"chats"`
}

type ChatComparison struct {
	Chat          string   `json:"chat"`
	Murmur        int      `json:"murmur"`
	LINA          int      `json:"lina"`
	MissingLINA   []string `json:"missing_lina"`
	MissingMurmur []string `json:"missing_murmur"`
}

type comparisonIDs map[string]map[string]bool

func (ids comparisonIDs) chat(chat string) map[string]bool {
	if ids[chat] == nil {
		ids[chat] = map[string]bool{}
	}
	return ids[chat]
}

// CompareMurmur reads a supplied, stable collector inbox and Core's canonical
// records in one read transaction. It never connects a source or changes its
// state. Deleted/erased IDs still prove collection; message content is not read.
func (s *Store) CompareMurmur(ctx context.Context, req ComparisonRequest, inbox string) (Comparison, error) {
	if !validName(req.Person) || !validName(req.Account) || req.Since.IsZero() || !req.Until.After(req.Since) {
		return Comparison{}, ErrInvalid
	}
	report := Comparison{Account: req.Account, Since: req.Since.UTC(), Until: req.Until.UTC(), Chats: []ChatComparison{}}
	lina := comparisonIDs{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var person string
		if err := tx.QueryRowContext(ctx, "SELECT person FROM messenger_source WHERE platform='kakaotalk' AND account=?", req.Account).Scan(&person); err != nil || person != req.Person {
			return ErrNotFound
		}
		rows, err := tx.QueryContext(ctx, "SELECT chat,message,occurred_at FROM messenger_record WHERE platform='kakaotalk' AND account=?", req.Account)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var chat, id, stamp string
			if err := rows.Scan(&chat, &id, &stamp); err != nil {
				return err
			}
			// A deletion of an unseen message has no occurrence time and cannot
			// establish collection within this window.
			if stamp == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				return errors.New("comparison_record_time_invalid")
			}
			if !at.Before(req.Since) && at.Before(req.Until) {
				lina.chat(chat)[id] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return Comparison{}, err
	}
	murmur, err := readComparisonInbox(ctx, inbox, req)
	if err != nil {
		return Comparison{}, err
	}
	chats := map[string]bool{}
	for chat := range lina {
		chats[chat] = true
	}
	for chat := range murmur {
		chats[chat] = true
	}
	keys := make([]string, 0, len(chats))
	for chat := range chats {
		keys = append(keys, chat)
	}
	sort.Strings(keys)
	for _, chat := range keys {
		row := ChatComparison{Chat: chat, Murmur: len(murmur[chat]), LINA: len(lina[chat]), MissingLINA: []string{}, MissingMurmur: []string{}}
		for id := range murmur[chat] {
			if !lina[chat][id] {
				row.MissingLINA = append(row.MissingLINA, id)
			}
		}
		for id := range lina[chat] {
			if !murmur[chat][id] {
				row.MissingMurmur = append(row.MissingMurmur, id)
			}
		}
		sort.Strings(row.MissingLINA)
		sort.Strings(row.MissingMurmur)
		report.Murmur += row.Murmur
		report.LINA += row.LINA
		report.MissingLINA += len(row.MissingLINA)
		report.MissingMurmur += len(row.MissingMurmur)
		report.Chats = append(report.Chats, row)
	}
	return report, nil
}
