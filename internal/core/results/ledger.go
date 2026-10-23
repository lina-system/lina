// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/result"
)

// LedgerJournal keeps a complete typed preview inline in conv_event, including
// bodies larger than generic ledger text's 16 KiB spill threshold. Result
// previews must have no material copy. It uses the ledger's native labels and
// masking boundary, and its ordinary reader validates the resulting events.
type LedgerJournal struct {
	Surface  ledger.Surface
	ParentID string // Trusted originating input, when composed into a live turn.
	Now      func() time.Time
}

func (j LedgerJournal) Append(ctx context.Context, tx *canon.Tx, s result.Scope, r result.Result) (string, error) {
	if !validScope(s) || r.ID == "" || r.Revision <= 0 {
		return "", result.ErrMalformed
	}
	var parent any
	if j.ParentID != "" {
		var matches int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM conv_event WHERE id=? AND agent_id=? AND conversation_id=?", j.ParentID, s.AgentID, s.ConversationID).Scan(&matches); err != nil {
			return "", err
		}
		if matches != 1 {
			return "", ErrOutOfScope
		}
		parent = j.ParentID
	}
	switch j.Surface.Kind {
	case "app", "tui", "launcher":
		if j.Surface.Platform != "" || j.Surface.ChatID != "" {
			return "", result.ErrMalformed
		}
	case "channel":
		if j.Surface.Platform == "" || j.Surface.ChatID == "" {
			return "", result.ErrMalformed
		}
	default:
		return "", result.ErrMalformed
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		return "", err
	}
	// The results flow masks before deriving text and returning its model card.
	// Refuse a caller that bypasses that boundary; never persist divergent bytes.
	if masked.Text() != string(raw) {
		return "", result.ErrMalformed
	}
	for _, v := range []string{s.PersonID, s.AgentID, s.ConversationID, s.TurnID, j.Surface.Platform, j.Surface.ChatID} {
		m, err := secrets.Mask(v)
		if err != nil || m.Text() != v || strings.ContainsRune(v, 0) {
			return "", result.ErrMalformed
		}
	}
	message, err := guard.Admit(guard.EntryAgent, "", string(raw))
	if err != nil {
		return "", err
	}
	surface, _ := json.Marshal(j.Surface)
	origin, _ := json.Marshal(ledger.Origin{Kind: "native", Entry: guard.EntryAgent})
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	stamp := now().UTC().Format(time.RFC3339Nano)
	id := newID()
	_, err = tx.ExecContext(ctx, `INSERT INTO conv_event
 (id,conversation_id,parent_id,agent_id,speaker_kind,surface,scaffold_kind,occurred_at,recorded_at,time_quality,origin,body)
 VALUES(?,?,?,?,?,?,'result',?,?,'write_time_only',?,?)`, id, s.ConversationID, parent, s.AgentID, message.SpeakerKind(), string(surface), stamp, stamp, string(origin), string(raw))
	return id, err
}

type transaction struct{ tx *canon.Tx }

func (t transaction) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(t.tx) }
func (t transaction) Write(_ context.Context, f func(*canon.Tx) error) error { return f(t.tx) }
func (j LedgerJournal) Read(ctx context.Context, tx *canon.Tx, s result.Scope, id string) (r result.Result, err error) {
	event, err := ledger.New(transaction{tx}, nil).Get(ctx, id)
	if err != nil {
		return r, err
	}
	if event.ConversationID != s.ConversationID || event.AgentID != s.AgentID || event.ScaffoldKind != "result" || event.SpeakerKind != guard.Agent {
		return r, ErrUnavailable
	}
	err = json.Unmarshal([]byte(event.Body.Text()), &r)
	return r, err
}

// Mask decoded string values so JSON escapes cannot conceal a secret and the
// renderer and model acknowledgement see the same masked result as the ledger.
func maskPresentation(p result.PresentRequest) (result.PresentRequest, error) {
	var tree any
	d := json.NewDecoder(strings.NewReader(string(p.Body)))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		return p, result.ErrMalformed
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			m, err := secrets.Mask(x)
			return m.Text(), err
		case []any:
			for i, v := range x {
				next, err := walk(v)
				if err != nil {
					return nil, err
				}
				x[i] = next
			}
		case map[string]any:
			for k, v := range x {
				next, err := walk(v)
				if err != nil {
					return nil, err
				}
				x[k] = next
			}
		}
		return v, nil
	}
	tree, err := walk(tree)
	if err != nil {
		return p, err
	}
	p.Body, err = json.Marshal(tree)
	if err != nil {
		return p, err
	}
	title, e1 := secrets.Mask(p.Title)
	summary, e2 := secrets.Mask(p.Summary)
	p.Title = title.Text()
	p.Summary = summary.Text()
	return p, errors.Join(e1, e2)
}
