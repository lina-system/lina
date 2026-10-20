// SPDX-License-Identifier: Apache-2.0
package eventstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	"github.com/thisisjun786/lina/kit/diag"
)

type Filter struct {
	Since          time.Time
	Until          time.Time
	Components     []diag.Component
	ConversationID diag.ID
	TaskID         diag.ID
	AgentID        diag.ID
	CauseCode      diag.CauseCode
	Results        []diag.Result
	ErrorClasses   []diag.ErrorClass
	Limit          int
	Cursor         *Cursor
}
type Page struct {
	Events     []diag.Event `json:"events"`
	NextCursor *Cursor      `json:"next_cursor"`
}
type position struct {
	Time   time.Time
	ID     diag.ID
	Filter [32]byte
}

// Cursor is an opaque continuation tied to the exact filters, excluding limit.
// It is not an access token; authorization belongs to the calling tool.
type Cursor struct{ position position }

func (c Cursor) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(c.position)
	if err != nil {
		return nil, diag.ErrInvalid
	}
	return json.Marshal(base64.RawURLEncoding.EncodeToString(b))
}
func (c *Cursor) UnmarshalJSON(data []byte) error {
	if len(data) > 1024 {
		return diag.ErrInvalid
	}
	var token string
	if err := json.Unmarshal(data, &token); err != nil {
		return diag.ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return diag.ErrInvalid
	}
	var p position
	if err := json.Unmarshal(b, &p); err != nil || p.Time.IsZero() || p.ID.IsZero() {
		return diag.ErrInvalid
	}
	c.position = p
	return nil
}

func (f Filter) validate() error {
	if f.Limit < 0 || f.Limit > 1000 || (!f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until)) {
		return diag.ErrInvalid
	}
	for _, timestamp := range []time.Time{f.Since, f.Until} {
		if _, err := timestamp.MarshalJSON(); err != nil {
			return diag.ErrInvalid
		}
	}
	for _, values := range []any{f.Components, f.Results, f.ErrorClasses} {
		if _, err := json.Marshal(values); err != nil {
			return diag.ErrInvalid
		}
	}
	if f.CauseCode != 0 {
		if _, err := json.Marshal(f.CauseCode); err != nil {
			return diag.ErrInvalid
		}
	}
	return nil
}
func (f Filter) fingerprint() [32]byte {
	f.Cursor = nil
	f.Limit = 0
	// CauseCode zero means omitted rather than a wire code.
	type wire Filter
	b, _ := json.Marshal(struct {
		wire
		CauseCode uint8
	}{wire(f), uint8(f.CauseCode)})
	return sha256.Sum256(b)
}
func (f Filter) matches(e diag.Event) bool {
	return (f.Since.IsZero() || !e.Time.Before(f.Since)) && (f.Until.IsZero() || e.Time.Before(f.Until)) &&
		(len(f.Components) == 0 || slices.Contains(f.Components, e.Component)) &&
		(f.ConversationID.IsZero() || e.ConversationID == f.ConversationID) && (f.TaskID.IsZero() || e.TaskID == f.TaskID) &&
		(f.AgentID.IsZero() || e.AgentID == f.AgentID) && (f.CauseCode == 0 || e.Cause.Code == f.CauseCode) &&
		(len(f.Results) == 0 || slices.Contains(f.Results, e.Result)) && (len(f.ErrorClasses) == 0 || slices.Contains(f.ErrorClasses, e.ErrorClass))
}

// Query scans validated persisted records and intersects every requested filter.
// It does no writes or output-time redaction. Time ties use event id ordering.
// Pagination is live keyset pagination, not a historical snapshot.
func (s *Store) Query(ctx context.Context, f Filter) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Page{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err := f.validate(); err != nil {
		return Page{}, err
	}
	fingerprint := f.fingerprint()
	if f.Cursor != nil && (f.Cursor.position.ID.IsZero() || f.Cursor.position.Filter != fingerprint) {
		return Page{}, diag.ErrInvalid
	}
	events, err := s.read(ctx)
	if err != nil {
		return Page{}, err
	}
	matches := []diag.Event{}
	for _, e := range events {
		if !f.matches(e) {
			continue
		}
		if f.Cursor != nil {
			p := f.Cursor.position
			if e.Time.Before(p.Time) || (e.Time.Equal(p.Time) && e.EventID.String() <= p.ID.String()) {
				continue
			}
		}
		matches = append(matches, e)
	}
	slices.SortFunc(matches, func(a, b diag.Event) int {
		if c := a.Time.Compare(b.Time); c != 0 {
			return c
		}
		if a.EventID.String() < b.EventID.String() {
			return -1
		}
		if a.EventID == b.EventID {
			return 0
		}
		return 1
	})
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	page := Page{Events: matches}
	if len(matches) > limit {
		page.Events = matches[:limit]
		last := page.Events[limit-1]
		page.NextCursor = &Cursor{position{last.Time, last.EventID, fingerprint}}
	}
	return page, nil
}
