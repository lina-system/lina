// SPDX-License-Identifier: Apache-2.0
package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type MessengerSource struct {
	Platform string `json:"platform"`
	Account  string `json:"account"`
}
type MessengerChat struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Count       int       `json:"count"`
	PersonCount int       `json:"person_count"`
	LastMessage time.Time `json:"last_message_time"`
}
type MessengerAuthor struct {
	ID     string `json:"platform_id"`
	Name   string `json:"name"`
	Person bool   `json:"is_person"`
}
type MessengerItem struct {
	DecryptionFailed   bool              `json:"decryption_failed,omitempty"`
	ID                 string            `json:"id"`
	Revision           int               `json:"revision"`
	Time               time.Time         `json:"time"`
	Author             MessengerAuthor   `json:"author"`
	Excerpt            string            `json:"excerpt"`
	LegacyReference    json.RawMessage   `json:"legacy_reference"`
	ImportedReferences []json.RawMessage `json:"imported_references,omitempty"`
	Unavailable        bool              `json:"unavailable,omitempty"`
}
type MessengerGap struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}
type MessengerCoverage struct {
	State      string         `json:"state"`
	ReadAt     time.Time      `json:"read_at"`
	Through    time.Time      `json:"through"`
	FreshUntil time.Time      `json:"fresh_until"`
	Gaps       []MessengerGap `json:"gaps"`
}
type MessengerPage struct {
	Source     MessengerSource   `json:"source"`
	Chat       MessengerChat     `json:"chat"`
	Items      []MessengerItem   `json:"items"`
	Coverage   MessengerCoverage `json:"coverage"`
	NextCursor string            `json:"next_cursor"`
}

// MessengerPages materializes only records selected by the shared recall scope.
// The retraction epoch fences material reads, which cannot hold a canon transaction.
func MessengerPages(ctx context.Context, db Canon, scope Scope, hits []Result, now time.Time) ([]MessengerPage, error) {
	if scope.Origin != "messenger" || validScope(scope) != nil {
		return nil, ErrInvalid
	}
	pages := []MessengerPage{}
	for _, hit := range hits {
		if hit.Kind != Event {
			continue
		}
		var item MessengerItem
		var source MessengerSource
		var chat, occurred, legacy string
		err := db.Read(ctx, func(tx *canon.Tx) error {
			err := tx.QueryRowContext(ctx, `SELECT m.platform,m.account,m.chat,m.event,m.revision,m.occurred_at,m.author,m.author_name,e.speaker_kind='person',m.decryption_failed,json_object('platform',m.platform,'account',m.account,'chat',m.chat,'message',m.message)
 FROM messenger_record m JOIN messenger_source s ON s.platform=m.platform AND s.account=m.account JOIN retract_visible_event e ON e.id=m.event WHERE m.event=? AND m.state='live' AND s.person=?`, hit.ID, scope.Person).Scan(&source.Platform, &source.Account, &chat, &item.ID, &item.Revision, &occurred, &item.Author.ID, &item.Author.Name, &item.Author.Person, &item.DecryptionFailed, &legacy)
			if err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, `SELECT json_object('system',l.system,'instance',l.instance,'kind',l.kind,'key',l.key) FROM messenger_legacy_ref l JOIN messenger_record m ON m.platform=l.platform AND m.account=l.account AND m.chat=l.chat AND m.message=l.message WHERE m.event=? ORDER BY l.system,l.instance,l.kind,l.key`, hit.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					return err
				}
				item.ImportedReferences = append(item.ImportedReferences, json.RawMessage(raw))
			}
			return rows.Err()
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		item.LegacyReference = json.RawMessage(legacy)
		item.Time, _ = time.Parse(time.RFC3339Nano, occurred)
		item.Excerpt = hit.Excerpt
		item.Unavailable = hit.Unavailable || item.DecryptionFailed
		index := -1
		for i, p := range pages {
			if p.Source == source && p.Chat.ID == chat {
				index = i
				break
			}
		}
		if index < 0 {
			p, err := messengerPage(ctx, db, scope.Person, source, chat, now)
			if err != nil {
				return nil, err
			}
			pages = append(pages, p)
			index = len(pages) - 1
		}
		pages[index].Items = append(pages[index].Items, item)
	}
	return pages, nil
}

func messengerPage(ctx context.Context, db Canon, person string, source MessengerSource, chat string, now time.Time) (MessengerPage, error) {
	p := MessengerPage{Source: source, Chat: MessengerChat{ID: chat}, Items: []MessengerItem{}, Coverage: MessengerCoverage{State: "stale", Gaps: []MessengerGap{}}}
	err := db.Read(ctx, func(tx *canon.Tx) error {
		var last sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT c.name,count(e.id),coalesce(sum(e.speaker_kind='person'),0),max(CASE WHEN e.id IS NOT NULL THEN m.occurred_at END) FROM messenger_chat c JOIN messenger_source s ON s.platform=c.platform AND s.account=c.account LEFT JOIN messenger_record m ON m.platform=c.platform AND m.account=c.account AND m.chat=c.chat AND m.state='live' LEFT JOIN retract_visible_event e ON e.id=m.event WHERE s.person=? AND c.platform=? AND c.account=? AND c.chat=? GROUP BY c.platform,c.account,c.chat`, person, source.Platform, source.Account, chat).Scan(&p.Chat.Name, &p.Chat.Count, &p.Chat.PersonCount, &last)
		if err != nil {
			return err
		}
		p.Chat.LastMessage, _ = time.Parse(time.RFC3339Nano, last.String)
		rows, err := tx.QueryContext(ctx, `SELECT read_at,read_through,interval_ns,window_ns FROM messenger_coverage WHERE platform=? AND account=? AND chat=? ORDER BY julianday(read_at),read_at`, source.Platform, source.Account, chat)
		if err != nil {
			return err
		}
		defer rows.Close()
		var previous time.Time
		var cadence, window time.Duration
		for rows.Next() {
			var at, through string
			if err := rows.Scan(&at, &through, &cadence, &window); err != nil {
				return err
			}
			readAt, _ := time.Parse(time.RFC3339Nano, at)
			if !previous.IsZero() && window > 0 && readAt.Sub(previous) > window {
				p.Coverage.Gaps = append(p.Coverage.Gaps, MessengerGap{Since: previous, Until: readAt.Add(-window)})
			}
			previous = readAt
			p.Coverage.ReadAt = readAt
			p.Coverage.Through, _ = time.Parse(time.RFC3339Nano, through)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !p.Coverage.ReadAt.IsZero() {
			p.Coverage.FreshUntil = p.Coverage.ReadAt.Add(cadence)
			if cadence > 0 && !now.After(p.Coverage.FreshUntil) {
				p.Coverage.State = "fresh"
			}
			if window > 0 && now.Sub(p.Coverage.ReadAt) > window {
				p.Coverage.Gaps = append(p.Coverage.Gaps, MessengerGap{Since: p.Coverage.Through, Until: now.Add(-window)})
			}
		}
		if len(p.Coverage.Gaps) > 0 {
			p.Coverage.State = "gap"
		}
		return nil
	})
	return p, err
}

func pageOffset(cursor string, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 || n > 10000 {
		return 0, ErrInvalid
	}
	return n, nil
}

// MessengerChats lists the authenticated person's chats, including empty chats.
func MessengerChats(ctx context.Context, db Canon, scope Scope, cursor string, limit int, now time.Time) ([]MessengerPage, error) {
	offset, err := pageOffset(cursor, limit)
	if err != nil || scope.Person == "" {
		return nil, ErrInvalid
	}
	type key struct {
		source MessengerSource
		chat   string
	}
	var keys []key
	err = db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.platform,c.account,c.chat FROM messenger_chat c JOIN messenger_source s ON s.platform=c.platform AND s.account=c.account WHERE s.person=? AND (?='' OR c.platform=?) AND (?='' OR c.account=?) AND (?='' OR c.chat=?) ORDER BY c.platform,c.account,c.chat LIMIT ? OFFSET ?`, scope.Person, scope.Platform, scope.Platform, scope.Account, scope.Account, scope.Chat, scope.Chat, limit+1, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.source.Platform, &k.source.Account, &k.chat); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	next := ""
	if len(keys) > limit {
		next = strconv.Itoa(offset + limit)
		keys = keys[:limit]
	}
	pages := []MessengerPage{}
	for _, k := range keys {
		p, err := messengerPage(ctx, db, scope.Person, k.source, k.chat, now)
		if err != nil {
			return nil, err
		}
		p.NextCursor = next
		pages = append(pages, p)
	}
	return pages, nil
}

// MessengerExpand pages a chronological neighborhood, starting up to five
// visible records before the anchor. Every page revalidates that exact anchor.
func MessengerExpand(ctx context.Context, db Canon, read ReadEvent, scope Scope, id string, offset, limit int, now time.Time) (MessengerPage, error) {
	if offset < 0 || offset > 10000 || limit < 1 || limit > 100 {
		return MessengerPage{}, ErrInvalid
	}
	if strings.HasPrefix(id, "messenger:messenger:") {
		id = strings.TrimPrefix(id, "messenger:")
	}
	scope.Origin = "messenger"
	scope.Kinds = []Kind{Event}
	var epoch uint64
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch)
	}); err != nil {
		return MessengerPage{}, err
	}
	var conversation string
	var source MessengerSource
	var chat string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		where, args := scopeWhere(scope, []Kind{Event}, now.Unix(), true, "d")
		args = append(args, id)
		prefix, prefixArgs := restrictedPrefix(scope)
		return tx.QueryRowContext(ctx, prefix+` SELECT d.conversation,m.platform,m.account,m.chat FROM recall_source d JOIN messenger_record m ON m.event=d.source_id WHERE `+strings.Join(where, " AND ")+` AND d.source_id=?`, append(prefixArgs, args...)...).Scan(&conversation, &source.Platform, &source.Account, &chat)
	})
	if err != nil {
		return MessengerPage{}, err
	}
	scope.Conversation = conversation
	var hits []Hit
	err = db.Read(ctx, func(tx *canon.Tx) error {
		where, args := scopeWhere(scope, []Kind{Event}, now.Unix(), true, "d")
		prefix, prefixArgs := restrictedPrefix(scope)
		rows, err := tx.QueryContext(ctx, prefix+`, ordered AS (SELECT d.source_id,d.revision,d.occurred_at,d.holder,row_number() OVER(ORDER BY julianday(d.occurred_at),d.source_id)-1 AS position FROM recall_source d WHERE `+strings.Join(where, " AND ")+`) SELECT source_id,revision,occurred_at,holder FROM ordered WHERE position>=max(0,(SELECT position FROM ordered WHERE source_id=?)-5)+? ORDER BY position LIMIT ?`, append(append(prefixArgs, args...), id, offset, limit+1)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h := Hit{Kind: Event}
			var at string
			if err := rows.Scan(&h.ID, &h.Revision, &at, &h.Holder); err != nil {
				return err
			}
			h.OccurredAt, _ = time.Parse(time.RFC3339Nano, at)
			hits = append(hits, h)
		}
		return rows.Err()
	})
	if err != nil {
		return MessengerPage{}, err
	}
	next := ""
	if len(hits) > limit {
		next = strconv.Itoa(offset + limit)
		hits = hits[:limit]
	}
	results := []Result{}
	for _, h := range hits {
		text, err := sourceText(ctx, db, read, h)
		if err != nil {
			return MessengerPage{}, err
		}
		// The search excerpt bound does not apply here. The next offset selects
		// other records, so a truncated body cannot be read on a later page.
		// sourceText already returns the masked message.
		results = append(results, Result{Hit: h, Excerpt: text})
	}
	pages, err := MessengerPages(ctx, db, scope, results, now)
	if err != nil {
		return MessengerPage{}, err
	}
	var p MessengerPage
	if len(pages) > 0 {
		p = pages[0]
	} else {
		p, err = messengerPage(ctx, db, scope.Person, source, chat, now)
		if err != nil {
			return p, err
		}
	}
	p.NextCursor = next
	err = db.Read(ctx, func(tx *canon.Tx) error {
		var current uint64
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&current); err != nil {
			return err
		}
		if current != epoch {
			return ErrInvalid
		}
		return nil
	})
	return p, err
}
