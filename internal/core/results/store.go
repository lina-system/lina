// SPDX-License-Identifier: Apache-2.0

// Package results composes result validation, ledger revisions and durable saves.
package results

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
)

var (
	ErrStaleRevision = errors.New("stale_revision")
	ErrUnavailable   = errors.New("unavailable")
	ErrOutOfScope    = errors.New("out_of_scope")
)

// Schema is installed by Core in a numbered migration after the ledger. Heads
// contain only locators; the ledger is the sole store of preview content.
const Schema = canon.ResultsSchema

type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

// Journal writes a complete revision in the same transaction as the head. It
// must use trusted native admission and preserve provenance and speaker labels.
type Journal interface {
	Append(context.Context, *canon.Tx, result.Scope, result.Result) (string, error)
	Read(context.Context, *canon.Tx, result.Scope, string) (result.Result, error)
}

// Writer resolves and authorizes a destination without publishing anything.
// Its returned operation retains that resolution through the durable claim.
// Both phases run outside canon transactions. Once publication starts, an error
// remains conservatively unknown and the store never dispatches it again.
type Writer interface {
	Prepare(context.Context, result.Scope, result.Result, result.Destination) (PreparedSave, error)
}
type PreparedSave func(context.Context, result.SavedRef) error
type Config struct {
	Journal   Journal
	Writer    Writer // Optional: nil permits previews and reads, but no saves.
	Limits    result.Limits
	TextRunes int
}
type Store struct {
	db  Canon
	cfg Config
}

func New(db Canon, cfg Config) (*Store, error) {
	if db == nil || cfg.Journal == nil {
		return nil, result.ErrMalformed
	}
	if cfg.TextRunes <= 0 {
		cfg.TextRunes = 2048
	}
	return &Store{db: db, cfg: cfg}, nil
}
func newID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(id[:])
}
func validScope(s result.Scope) bool {
	return s.PersonID != "" && s.AgentID != "" && s.ConversationID != "" && s.TurnID != "" && (s.TaskID == nil || *s.TaskID != "")
}
func (s *Store) head(ctx context.Context, tx *canon.Tx, scope result.Scope, id string) (r result.Result, key string, err error) {
	var event string
	var save sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT event_id,save_key FROM result_heads WHERE id=? AND person=? AND agent=? AND conversation=?", id, scope.PersonID, scope.AgentID, scope.ConversationID).Scan(&event, &save)
	if errors.Is(err, sql.ErrNoRows) {
		return r, "", ErrUnavailable
	}
	if err != nil {
		return r, "", err
	}
	r, err = s.cfg.Journal.Read(ctx, tx, scope, event)
	return r, save.String, err
}
func (s *Store) Present(ctx context.Context, scope result.Scope, p result.PresentRequest) (result.Card, error) {
	if !result.ValidReplyType(p.ReplyType) || !validScope(scope) || strings.TrimSpace(p.Title) == "" || utf8.RuneCountInString(p.Title) > 200 || strings.TrimSpace(p.Summary) == "" || utf8.RuneCountInString(p.Summary) > 600 || p.ID == "" && p.BaseRevision != 0 || p.ID != "" && p.BaseRevision <= 0 {
		return result.Card{}, result.ErrMalformed
	}
	// Reject invalid input before decoding it for masking; duplicate fields must
	// never be collapsed into an apparently valid body.
	if _, err := result.Validate(p.Type, p.Body, s.cfg.Limits); err != nil {
		return result.Card{}, err
	}
	p, err := maskPresentation(p)
	if err != nil {
		return result.Card{}, err
	}
	body, err := result.Validate(p.Type, p.Body, s.cfg.Limits)
	if err != nil {
		return result.Card{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return result.Card{}, err
	}
	r := result.Result{ReplyType: p.ReplyType, ID: p.ID, Revision: 1, Type: p.Type, Title: p.Title, Summary: p.Summary, Text: result.PlainText(body, nil), Body: raw, Status: "preview", RevisedBy: scope.AgentID, Provenance: result.Provenance{AgentID: scope.AgentID, ConversationID: scope.ConversationID, TurnID: scope.TurnID, TaskID: scope.TaskID}}
	if r.ID == "" {
		r.ID = newID()
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if p.ID != "" {
			var managed int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM plan_drafts WHERE result_id=?", p.ID).Scan(&managed); err != nil {
				return err
			}
			if managed != 0 {
				return ErrOutOfScope
			}
			old, key, err := s.head(ctx, tx, scope, p.ID)
			if err != nil {
				return err
			}
			if old.Revision != p.BaseRevision {
				return ErrStaleRevision
			}
			if key != "" || old.Status == "saved" {
				return ErrOutOfScope
			}
			r.Revision = old.Revision + 1
			if old.Completion != nil && p.Completion == nil {
				p.Completion = &result.CompletionRequest{WorkKind: old.Completion.WorkKind}
			}
		}
		if p.Completion != nil {
			c := result.CompletionFor(ctx, *p.Completion)
			c.ID, c.AgentID = "result:"+r.ID+":"+strconv.Itoa(r.Revision), scope.AgentID
			r.Completion = &c
			if err := canon.RecordDoneCompletion(ctx, tx, r.Completion); err != nil {
				return err
			}
		}
		event, err := s.cfg.Journal.Append(ctx, tx, scope, r)
		if err != nil {
			return err
		}
		if p.ID == "" {
			_, err = tx.ExecContext(ctx, "INSERT INTO result_heads(id,person,agent,conversation,revision,event_id) VALUES(?,?,?,?,?,?)", r.ID, scope.PersonID, scope.AgentID, scope.ConversationID, r.Revision, event)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE result_heads SET revision=?,event_id=? WHERE id=?", r.Revision, event, r.ID)
		}
		return err
	})
	if err != nil {
		return result.Card{}, err
	}
	text := []rune(r.Text)
	if len(text) > s.cfg.TextRunes {
		text = text[:s.cfg.TextRunes]
	}
	return result.Card{ID: r.ID, Revision: r.Revision, Type: r.Type, Title: r.Title, Summary: r.Summary, Text: string(text), Completion: r.Completion}, nil
}
func (s *Store) Read(ctx context.Context, scope result.Scope, p result.ReadRequest) (r result.Result, err error) {
	if !validScope(scope) || p.ID == "" {
		return r, result.ErrMalformed
	}
	err = s.db.Read(ctx, func(tx *canon.Tx) error { var err error; r, _, err = s.head(ctx, tx, scope, p.ID); return err })
	if err != nil {
		return result.Result{}, err
	}
	return r, nil
}
func destination(kind result.Type, d result.Destination) (result.Destination, error) {
	switch kind {
	case result.SummaryType:
		if d.Kind == "" {
			d.Kind = "memo"
		}
		if d.Kind != "memo" || d.Container != "" {
			return d, ErrOutOfScope
		}
	case result.TableType, result.DiagramType, result.ChartType, result.DraftType, result.FileType:
		if d.Kind == "" {
			d.Kind = "resource"
		}
		switch d.Kind {
		case "resource":
			if d.Container != "" {
				return d, ErrOutOfScope
			}
		case "project", "area", "goal":
			if d.Container == "" {
				return d, ErrOutOfScope
			}
		default:
			return d, ErrOutOfScope
		}
	case result.ComparisonType, result.PlanType:
		return d, ErrOutOfScope
	default:
		return d, result.ErrMalformed
	}
	return d, nil
}

// SaveKey hashes the unambiguous CP-05 tuple. It excludes call id, turn and epoch
// so a double click or an identical retry has the same durable effect identity.
func SaveKey(id string, revision int, d result.Destination) string {
	raw, _ := json.Marshal([]any{id, revision, "result_save", d})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Save is a trusted Core operation. The exec binding admits it only for the
// person's matching request in this turn; model arguments cannot grant that.
func (s *Store) Save(ctx context.Context, scope result.Scope, p result.SaveRequest) (receipt result.SaveReceipt, err error) {
	receipt.State = "refused"
	if !validScope(scope) || p.ID == "" || p.Revision <= 0 {
		return receipt, result.ErrMalformed
	}
	var snapshot result.Result
	var prior string
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		snapshot, prior, err = s.head(ctx, tx, scope, p.ID)
		return err
	})
	if err != nil {
		return receipt, err
	}
	if snapshot.Revision != p.Revision {
		return receipt, ErrStaleRevision
	}
	dest, err := destination(snapshot.Type, p.Destination)
	if err != nil {
		return receipt, err
	}
	key := SaveKey(p.ID, p.Revision, dest)
	var publish PreparedSave
	if prior == "" {
		if s.cfg.Writer == nil {
			return receipt, ErrOutOfScope
		}
		publish, err = s.cfg.Writer.Prepare(ctx, scope, snapshot, dest)
		if err != nil {
			return receipt, err
		}
		if publish == nil {
			return receipt, ErrUnavailable
		}
	}
	var r result.Result
	var ref result.SavedRef
	claimed := false
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		var existing string
		var err error
		r, existing, err = s.head(ctx, tx, scope, p.ID)
		if err != nil {
			return err
		}
		if r.Revision != p.Revision {
			return ErrStaleRevision
		}
		if existing != "" {
			if existing != key {
				return ErrOutOfScope
			}
			var raw []byte
			if err := tx.QueryRowContext(ctx, "SELECT state,reference FROM result_saves WHERE key=?", key).Scan(&receipt.State, &raw); err != nil {
				return err
			}
			if receipt.State == "applied" {
				if err := json.Unmarshal(raw, &ref); err != nil {
					return err
				}
				receipt.SavedAs = &ref
			}
			return nil
		}
		if publish == nil {
			return ErrUnavailable
		}
		ref = result.SavedRef{ID: newID(), Version: 1, Destination: dest}
		raw, _ := json.Marshal(ref)
		if _, err = tx.ExecContext(ctx, "INSERT INTO result_saves(key,result_id,state,reference) VALUES(?,?,'unknown',?)", key, p.ID, raw); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE result_heads SET save_key=? WHERE id=?", key, p.ID)
		claimed = err == nil
		return err
	})
	if err != nil {
		return result.SaveReceipt{State: "refused"}, err
	}
	if !claimed {
		return receipt, nil
	}
	receipt = result.SaveReceipt{State: "unknown"}
	if err = publish(ctx, ref); err != nil {
		return receipt, err
	}
	r.Status = "saved"
	r.SavedAs = &ref
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		event, err := s.cfg.Journal.Append(ctx, tx, scope, r)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE result_heads SET event_id=? WHERE id=? AND save_key=?", event, r.ID, key); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE result_saves SET state='applied' WHERE key=?", key)
		return err
	})
	if err != nil {
		return receipt, err
	}
	return result.SaveReceipt{State: "applied", SavedAs: &ref}, nil
}
