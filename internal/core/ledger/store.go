// SPDX-License-Identifier: Apache-2.0

// Package ledger owns immutable conversation events and their native admission.
// Core opens canon with canon.Migrations before constructing a Store.
package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

const InlineBytes = 16 << 10

var (
	ErrInvalid          = errors.New("invalid_ledger_event")
	ErrNotFound         = sql.ErrNoRows
	ErrBytesUnavailable = errors.New("bytes_unavailable")
)

type Surface struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}

// Origin preserves the trusted native entry even when two entry paths share a
// speaker kind. Import provenance is reserved for the separate importer.
type Origin struct {
	Kind    string      `json:"kind"`
	Entry   guard.Entry `json:"entry"`
	Locator string      `json:"locator,omitempty"`
}

type Relation struct {
	TargetID string
	Kind     string
	Ordinal  int
}

type Material struct {
	ID       string
	Revision int64
}

// Materials is supplied by Core's composition layer, which owns the assets
// store and opens its material writer before admission to recover interrupted
// preparations. Prepare syncs only masked bytes, without retained metadata;
// Insert publishes that metadata in the event's transaction. Read verifies the
// immutable revision's size and digest.
type Materials interface {
	Prepare(context.Context, secrets.Masked) (MaterialWrite, error)
	Read(context.Context, Material) (string, error)
}

type MaterialWrite interface {
	Insert(context.Context, *canon.Tx) (Material, error)
	// Close runs after the enclosing transaction, reconciling bytes against
	// committed canon. A durable marker must survive interrupted cleanup.
	Close() error
}

type Input struct {
	ID, ConversationID, ParentID string
	PersonID, AgentID            string
	Surface                      Surface
	ScaffoldKind, ScaffoldID     string
	OccurredAt, RecordedAt       time.Time
	TimeQuality                  string
	Locator, ReplayOf, Body      string
	Relations                    []Relation
	// SecretSpans address the original input. Tags address the masked text,
	// so no original secret offset or length can be retained in the ledger.
	SecretSpans []secrets.Span
	Tags        []secrets.Span // personal_id or third_party; never removes text
}

type Event struct {
	ID, ConversationID, ParentID string
	PersonID, AgentID            string
	SpeakerKind                  guard.SpeakerKind
	Surface                      Surface
	ScaffoldKind, ScaffoldID     string
	OccurredAt, RecordedAt       time.Time
	TimeQuality                  string
	Origin                       Origin
	ReplayOf                     string
	Body                         secrets.Masked
	Material                     Material
	BytesUnavailable             bool // read-time state; does not rewrite the event
	Relations                    []Relation
	Spans                        []secrets.Span
}

// Message reconstructs the model-bound evidence boundary after a restart,
// excluding secret spans. A missing material cannot become an empty message.
func (e Event) Message() (guard.Message, error) {
	if e.BytesUnavailable {
		return guard.Message{}, ErrBytesUnavailable
	}
	m, err := guard.Admit(e.Origin.Entry, e.Origin.Locator, e.Body.DerivedText())
	if err != nil {
		return guard.Message{}, err
	}
	if e.Origin.Kind != "native" || m.SpeakerKind() != e.SpeakerKind {
		return guard.Message{}, ErrInvalid
	}
	return m, nil
}

type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

type Store struct {
	db        Canon
	materials Materials
}

func New(db Canon, materials Materials) *Store { return &Store{db: db, materials: materials} }

// Admission binds an event to its trusted entry path.
type Admission struct {
	Entry guard.Entry
	Input Input
}

// Admit never accepts a speaker kind from the payload. The authenticated entry
// adapter supplies entry and identity fields. It acknowledges only after commit.
func (s *Store) Admit(ctx context.Context, entry guard.Entry, in Input) (Event, error) {
	events, err := s.AdmitBatch(ctx, []Admission{{Entry: entry, Input: in}})
	if err != nil {
		return Event{}, err
	}
	return events[0], nil
}

// AdmitBatch commits an ordered group of related events in one transaction.
// Later events may reference earlier events in the batch. Input validation,
// masking and durable byte preparation finish before the canon transaction.
// Material metadata, events, relations and spans commit together; Close
// reconciles staged bytes after success or failure, including interrupted cleanup.
func (s *Store) AdmitBatch(ctx context.Context, inputs []Admission) (_ []Event, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, ErrInvalid
	}
	events := make([]Event, len(inputs))
	for i, in := range inputs {
		events[i], err = prepare(in.Entry, in.Input)
		if err != nil {
			return nil, err
		}
	}
	writes := make([]MaterialWrite, len(events))
	defer func() {
		for _, w := range writes {
			if w == nil {
				continue
			}
			cleanupErr := w.Close()
			if err != nil {
				err = errors.Join(err, cleanupErr)
			}
			// A housekeeping error cannot revoke an already committed batch.
			// The durable material marker allows recovery after interrupted cleanup.
		}
	}()
	for i, e := range events {
		if len(e.Body.Text()) <= InlineBytes {
			continue
		}
		if s.materials == nil {
			return nil, ErrBytesUnavailable
		}
		writes[i], err = s.materials.Prepare(ctx, e.Body)
		if err != nil {
			return nil, err
		}
		if writes[i] == nil {
			return nil, ErrInvalid
		}
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		for i := range events {
			e := &events[i]
			if writes[i] != nil {
				var err error
				e.Material, err = writes[i].Insert(ctx, tx)
				if err != nil {
					return err
				}
				if !safeID(e.Material.ID) || e.Material.Revision <= 0 {
					return ErrInvalid
				}
			}
			if err := writeEvent(ctx, tx, *e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func writeEvent(ctx context.Context, tx *canon.Tx, e Event) error {
	if err := references(ctx, tx, e); err != nil {
		return err
	}
	surface, _ := json.Marshal(e.Surface)
	origin, _ := json.Marshal(e.Origin)
	var body any = e.Body.Text()
	var material, revision any
	if e.Material.ID != "" {
		body = nil
		material = e.Material.ID
		revision = e.Material.Revision
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO conv_event
	 (id,conversation_id,parent_id,person_id,agent_id,speaker_kind,surface,scaffold_kind,scaffold_id,occurred_at,recorded_at,time_quality,origin,replay_of,body,material_id,material_revision)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ConversationID, nullable(e.ParentID), nullable(e.PersonID), e.AgentID, e.SpeakerKind, string(surface), nullable(e.ScaffoldKind), nullable(e.ScaffoldID),
		e.OccurredAt.Format(time.RFC3339Nano), e.RecordedAt.Format(time.RFC3339Nano), e.TimeQuality, string(origin), nullable(e.ReplayOf), body, material, revision)
	if err != nil {
		return err
	}
	for _, r := range e.Relations {
		if _, err := tx.ExecContext(ctx, "INSERT INTO ev_rel(event_id,target_id,kind,ordinal) VALUES(?,?,?,?)", e.ID, r.TargetID, r.Kind, r.Ordinal); err != nil {
			return err
		}
	}
	for _, span := range e.Spans {
		if _, err := tx.ExecContext(ctx, "INSERT INTO ev_span(event_id,start,end,sensitivity) VALUES(?,?,?,?)", e.ID, span.Start, span.End, span.Sensitivity); err != nil {
			return err
		}
	}
	return indexText(ctx, tx, e.ID, e.Body.DerivedText())
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Reject secret-shaped metadata rather than silently changing an identifier or
// source locator. All textual fields are checked before the material callback.
func safeText(s string) bool {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return false
	}
	m, err := secrets.Mask(s)
	return err == nil && m.Text() == s
}
func safeID(s string) bool { return strings.TrimSpace(s) != "" && safeText(s) }

func prepare(entry guard.Entry, in Input) (Event, error) {
	m, err := guard.Admit(entry, in.Locator, in.Body)
	if err != nil {
		return Event{}, err
	}
	if !safeID(in.ID) || !safeID(in.ConversationID) || !safeID(in.AgentID) ||
		(m.SpeakerKind() == guard.Person && !safeID(in.PersonID)) || (m.SpeakerKind() != guard.Person && in.PersonID != "") {
		return Event{}, ErrInvalid
	}
	for _, text := range []string{in.ParentID, in.ScaffoldKind, in.ScaffoldID, in.ReplayOf, in.Locator, in.Surface.Platform, in.Surface.ChatID} {
		if !safeText(text) {
			return Event{}, ErrInvalid
		}
	}
	switch in.Surface.Kind {
	case "tui", "app", "agent_exchange":
		if in.Surface.Platform != "" || in.Surface.ChatID != "" {
			return Event{}, ErrInvalid
		}
	case "channel":
		if !safeID(in.Surface.Platform) || !safeID(in.Surface.ChatID) {
			return Event{}, ErrInvalid
		}
	default:
		return Event{}, ErrInvalid
	}
	if in.OccurredAt.IsZero() || in.RecordedAt.IsZero() || in.OccurredAt.Year() < 1 || in.OccurredAt.Year() > 9999 || in.RecordedAt.Year() < 1 || in.RecordedAt.Year() > 9999 {
		return Event{}, ErrInvalid
	}
	if in.TimeQuality != "observed" && in.TimeQuality != "recorded" && in.TimeQuality != "write_time_only" {
		return Event{}, ErrInvalid
	}
	if in.ScaffoldID != "" && in.ScaffoldKind == "" {
		return Event{}, ErrInvalid
	}
	body, err := secrets.Mask(in.Body, in.SecretSpans...)
	if err != nil {
		return Event{}, err
	}
	spans := body.Spans()
	for _, tag := range in.Tags {
		if (tag.Sensitivity != "personal_id" && tag.Sensitivity != "third_party") || !validSpan(body.Text(), tag) {
			return Event{}, ErrInvalid
		}
		spans = append(spans, tag)
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		if spans[i].End != spans[j].End {
			return spans[i].End < spans[j].End
		}
		return spans[i].Sensitivity < spans[j].Sensitivity
	})
	for _, r := range in.Relations {
		if !safeID(r.TargetID) || r.Ordinal < 0 || (r.Kind != "view_of" && r.Kind != "supersedes" && r.Kind != "pairs_with") || (r.Kind != "view_of" && r.Ordinal != 0) {
			return Event{}, ErrInvalid
		}
	}
	return Event{ID: in.ID, ConversationID: in.ConversationID, ParentID: in.ParentID, PersonID: in.PersonID, AgentID: in.AgentID, SpeakerKind: m.SpeakerKind(),
		Surface: in.Surface, ScaffoldKind: in.ScaffoldKind, ScaffoldID: in.ScaffoldID, OccurredAt: in.OccurredAt.UTC(), RecordedAt: in.RecordedAt.UTC(), TimeQuality: in.TimeQuality,
		Origin: Origin{Kind: "native", Entry: entry, Locator: in.Locator}, ReplayOf: in.ReplayOf, Body: body, Relations: append([]Relation(nil), in.Relations...), Spans: spans}, nil
}

func validSpan(text string, span secrets.Span) bool {
	return span.Start >= 0 && span.Start < span.End && span.End <= len(text) && utf8.RuneStart(text[span.Start]) && (span.End == len(text) || utf8.RuneStart(text[span.End]))
}

func references(ctx context.Context, tx *canon.Tx, e Event) error {
	refs := []string{e.ParentID, e.ReplayOf, e.ScaffoldID}
	for _, r := range e.Relations {
		refs = append(refs, r.TargetID)
	}
	for _, id := range refs {
		if id == "" {
			continue
		}
		var conversation string
		var scaffold sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT conversation_id,scaffold_kind FROM conv_event WHERE id=?", id).Scan(&conversation, &scaffold); err != nil {
			return err
		}
		if id == e.ParentID && conversation != e.ConversationID {
			return ErrInvalid
		}
		if id == e.ScaffoldID && scaffold.String != e.ScaffoldKind {
			return ErrInvalid
		}
	}
	return nil
}

// Get reads one committed snapshot of metadata, relations and spans. Material
// I/O happens after that transaction ends and never holds the canon writer.
// Missing/unverifiable bytes return the metadata with BytesUnavailable and an
// error; callers must not render or assemble that event as complete.
func (s *Store) Get(ctx context.Context, id string) (Event, error) {
	var e Event
	var text string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var parent, person, scaffold, scaffoldID, replay, body, material sql.NullString
		var revision sql.NullInt64
		var surface, origin, occurred, recorded string
		err := tx.QueryRowContext(ctx, `SELECT id,conversation_id,parent_id,person_id,agent_id,speaker_kind,surface,scaffold_kind,scaffold_id,
		 occurred_at,recorded_at,time_quality,origin,replay_of,body,material_id,material_revision FROM conv_event WHERE id=?`, id).
			Scan(&e.ID, &e.ConversationID, &parent, &person, &e.AgentID, &e.SpeakerKind, &surface, &scaffold, &scaffoldID, &occurred, &recorded, &e.TimeQuality, &origin, &replay, &body, &material, &revision)
		if err != nil {
			return err
		}
		e.ParentID, e.PersonID, e.ScaffoldKind, e.ScaffoldID, e.ReplayOf = parent.String, person.String, scaffold.String, scaffoldID.String, replay.String
		e.Material = Material{ID: material.String, Revision: revision.Int64}
		text = body.String
		if err := json.Unmarshal([]byte(surface), &e.Surface); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(origin), &e.Origin); err != nil {
			return err
		}
		if e.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred); err != nil {
			return err
		}
		if e.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT target_id,kind,ordinal FROM ev_rel WHERE event_id=? ORDER BY kind,ordinal,target_id", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r Relation
			if err := rows.Scan(&r.TargetID, &r.Kind, &r.Ordinal); err != nil {
				rows.Close()
				return err
			}
			e.Relations = append(e.Relations, r)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, "SELECT start,end,sensitivity FROM ev_span WHERE event_id=? ORDER BY start,end,sensitivity", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var span secrets.Span
			if err := rows.Scan(&span.Start, &span.End, &span.Sensitivity); err != nil {
				return err
			}
			e.Spans = append(e.Spans, span)
		}
		return rows.Err()
	})
	if err != nil {
		return Event{}, err
	}
	if e.Material.ID != "" {
		if s.materials == nil {
			e.BytesUnavailable = true
			return e, ErrBytesUnavailable
		}
		text, err = s.materials.Read(ctx, e.Material)
		if err != nil {
			e.BytesUnavailable = true
			return e, errors.Join(ErrBytesUnavailable, err)
		}
	}
	var secretSpans []secrets.Span
	for _, span := range e.Spans {
		if !validSpan(text, span) {
			return Event{}, ErrInvalid
		}
		if span.Sensitivity == "secret" {
			if text[span.Start:span.End] != secret.Placeholder {
				return Event{}, ErrInvalid
			}
			secretSpans = append(secretSpans, span)
		}
	}
	e.Body, err = secrets.Mask(text, secretSpans...)
	if err != nil || e.Body.Text() != text {
		return Event{}, ErrInvalid
	}
	if e.Origin.Kind == "import" {
		var sealed int
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM import_seal WHERE conversation_id=?", e.ConversationID).Scan(&sealed)
		})
		if err != nil {
			return Event{}, err
		}
		if sealed != 1 || e.Surface.Kind != "import" {
			return Event{}, ErrInvalid
		}
	} else if _, err := e.Message(); err != nil {
		return Event{}, err
	}
	return e, nil
}
