// SPDX-License-Identifier: Apache-2.0

// Package waiting builds the person's waiting list from its authoritative sources.
package waiting

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

var (
	ErrInvalid  = errors.New("invalid_waiting_item")
	ErrStale    = errors.New("stale_waiting_item")
	ErrPerson   = errors.New("waiting_person_mismatch")
	ErrAnswered = errors.New("waiting_already_answered")
)

type Reason string

const (
	OutsideEffect      Reason = "outside_effect"
	SignificantChoice  Reason = "significant_choice"
	MissingInformation Reason = "missing_information"
	PersonalAction     Reason = "personal_action"
)

type Order string

const (
	Priority Order = "priority"
	Lightest Order = "lightest"
)

type Ref struct {
	Source, ID string
	Revision   uint64
}

type Item struct {
	Person, Agent                                      string
	Revision                                           uint64
	View                                               view.Waiting
	Reason                                             Reason
	Finished, Blocking, Irreversible, Costly, Promised bool
	Requested                                          time.Time
}

// Source callbacks run inside one canon transaction. Candidates are only keys;
// Read must load the current source, including ownership and answer choices.
// Answer changes that source in the supplied transaction, with no external I/O.
// Done marks a completed candidate handled so it never appears as a waiting row.
type Source interface {
	Candidates(context.Context, *canon.Tx, string) ([]string, error)
	Read(context.Context, *canon.Tx, string) (Item, error)
	Done(context.Context, *canon.Tx, string) error
}

// TransactionAnswerer changes a producer's source in waiting's transaction.
type TransactionAnswerer interface {
	Answer(context.Context, *canon.Tx, string, string) error
}

// RecordedAnswerer is for an authority service that owns both authorization
// and its already-attributed decision transaction. Waiting never duplicates
// that policy or commits an approval independently of the authority service.
type RecordedAnswerer interface {
	AnswerRecorded(context.Context, string, Ref, string) (Decision, error)
	RecordedDecisions(context.Context, *canon.Tx, string) ([]Decision, error)
}

type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

type Entry struct {
	Ref  Ref
	View view.Waiting
}
type List struct {
	Entries     []Entry
	Total, More int
}
type Options struct {
	Order Order
	Limit int
	Now   time.Time
}
type Decision struct {
	Ref                   Ref
	Person, Agent, Answer string
	At                    time.Time
}

type Service struct {
	db      Canon
	sources map[string]Source
}

func New(db Canon, sources map[string]Source) *Service {
	return &Service{db: db, sources: cloneSources(sources)}
}

// Schema is installed by the composition root in a numbered canon migration.
const Schema = `CREATE TABLE waiting_decisions (
 source TEXT NOT NULL, item_id TEXT NOT NULL, revision TEXT NOT NULL,
 person TEXT NOT NULL, agent TEXT NOT NULL, answer TEXT NOT NULL, answered_at TEXT NOT NULL,
 PRIMARY KEY(source,item_id,revision)
);`

// Build re-reads every candidate before projecting it. A source failure refuses
// the whole snapshot rather than presenting an incomplete list as authoritative.
// Limit zero returns the full list; Home requests five.
func (s *Service) Build(ctx context.Context, person string, opts Options) (List, error) {
	if person == "" || opts.Now.IsZero() || opts.Limit < 0 || (opts.Order != "" && opts.Order != Priority && opts.Order != Lightest) {
		return List{}, ErrInvalid
	}
	type ranked struct {
		entry Entry
		item  Item
	}
	var rows []ranked
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		names := make([]string, 0, len(s.sources))
		for name := range s.sources {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			source := s.sources[name]
			if name == "" || source == nil {
				return ErrInvalid
			}
			keys, err := source.Candidates(ctx, tx, person)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, id := range keys {
				if id == "" {
					return ErrInvalid
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				item, err := source.Read(ctx, tx, id)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				if item.Person != person {
					continue
				}
				if item.Finished {
					if err := source.Done(ctx, tx, id); err != nil {
						return err
					}
					continue
				}
				if !eligible(item) {
					continue
				}
				ref := Ref{Source: name, ID: id, Revision: item.Revision}
				answered, err := hasDecision(ctx, tx, ref)
				if err != nil {
					return err
				}
				if answered {
					continue
				}
				v := item.View
				v.Answers = slices.Clone(v.Answers)
				rows = append(rows, ranked{Entry{Ref: ref, View: v}, item})
			}
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].item, rows[j].item
		if opts.Order == Lightest && a.View.Effort != b.View.Effort {
			return a.View.Effort < b.View.Effort
		}
		if x, y := urgency(a, opts.Now), urgency(b, opts.Now); x != y {
			return x < y
		}
		if x, y := a.Irreversible || a.Costly, b.Irreversible || b.Costly; x != y {
			return x
		}
		if !a.View.Due.Equal(b.View.Due) {
			return earlier(a.View.Due, b.View.Due)
		}
		if !a.Requested.Equal(b.Requested) {
			return earlier(a.Requested, b.Requested)
		}
		if rows[i].entry.Ref.Source != rows[j].entry.Ref.Source {
			return rows[i].entry.Ref.Source < rows[j].entry.Ref.Source
		}
		return rows[i].entry.Ref.ID < rows[j].entry.Ref.ID
	})
	list := List{Entries: []Entry{}, Total: len(rows)}
	count := len(rows)
	if opts.Limit > 0 && opts.Limit < count {
		count = opts.Limit
	}
	list.More = list.Total - count
	for _, r := range rows[:count] {
		list.Entries = append(list.Entries, r.entry)
	}
	return list, nil
}

func eligible(i Item) bool {
	if i.Person == "" || i.Agent == "" || i.Revision == 0 || i.Finished || i.View.Sentence == "" || len(i.View.Answers) == 0 || i.View.Effort < view.FiveMinutes || i.View.Effort > view.Longer {
		return false
	}
	switch i.Reason {
	case OutsideEffect, SignificantChoice, MissingInformation, PersonalAction:
	default:
		return false
	}
	switch i.View.Kind {
	case view.Decide, view.Reply:
		return true
	case view.Do:
		return i.Promised || !i.View.Due.IsZero()
	}
	return false
}
func urgency(i Item, now time.Time) int {
	due := i.View.Due
	if i.Blocking || (!due.IsZero() && !due.Before(now) && !due.After(now.Add(24*time.Hour))) {
		return 0
	}
	if !due.IsZero() && due.Before(now) {
		return 1
	}
	return 2
}
func earlier(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	if b.IsZero() {
		return true
	}
	return a.Before(b)
}
func hasDecision(ctx context.Context, tx *canon.Tx, ref Ref) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM waiting_decisions WHERE source=? AND item_id=? AND revision=?", ref.Source, ref.ID, strconv.FormatUint(ref.Revision, 10)).Scan(&n)
	return n != 0, err
}

// Answer takes the person established by Core's authenticated entry path, never
// an identity supplied by the row's answer payload. The agent comes from Read.
// Source.Answer must also enforce any source-specific authorization (notably
// approval policy); a waiting-list selection is not itself an approval grant.
func (s *Service) Answer(ctx context.Context, person string, ref Ref, value string, at time.Time) (Decision, error) {
	source := s.sources[ref.Source]
	if person == "" || ref.ID == "" || ref.Revision == 0 || source == nil || at.IsZero() {
		return Decision{}, ErrInvalid
	}
	if recorder, ok := source.(RecordedAnswerer); ok {
		decision, err := recorder.AnswerRecorded(ctx, person, ref, value)
		if err != nil {
			return Decision{}, err
		}
		decision.Ref.Source = ref.Source
		return decision, nil
	}
	answerer, ok := source.(TransactionAnswerer)
	if !ok {
		return Decision{}, ErrInvalid
	}
	var decision Decision
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		item, err := source.Read(ctx, tx, ref.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return err
		}
		if item.Person != person {
			return ErrPerson
		}
		answered, err := hasDecision(ctx, tx, ref)
		if err != nil {
			return err
		}
		if answered {
			return ErrAnswered
		}
		if item.Revision != ref.Revision || !eligible(item) {
			return ErrStale
		}
		if !slices.Contains(item.View.Answers, value) {
			return ErrInvalid
		}
		if err := answerer.Answer(ctx, tx, ref.ID, value); err != nil {
			return err
		}
		decision = Decision{Ref: ref, Person: person, Agent: item.Agent, Answer: value, At: at.UTC()}
		_, err = tx.ExecContext(ctx, "INSERT INTO waiting_decisions(source,item_id,revision,person,agent,answer,answered_at) VALUES(?,?,?,?,?,?,?)", ref.Source, ref.ID, strconv.FormatUint(ref.Revision, 10), person, item.Agent, value, decision.At.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return Decision{}, err
	}
	return decision, nil
}
func (s *Service) Decisions(ctx context.Context, person string) ([]Decision, error) {
	if person == "" {
		return nil, ErrInvalid
	}
	decisions := []Decision{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT source,item_id,revision,person,agent,answer,answered_at FROM waiting_decisions WHERE person=? ORDER BY answered_at,source,item_id,revision", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Decision
			var revision, at string
			if err := rows.Scan(&d.Ref.Source, &d.Ref.ID, &revision, &d.Person, &d.Agent, &d.Answer, &at); err != nil {
				return err
			}
			d.Ref.Revision, err = strconv.ParseUint(revision, 10, 64)
			if err != nil {
				return err
			}
			d.At, err = time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return err
			}
			decisions = append(decisions, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for name, source := range s.sources {
			if recorder, ok := source.(RecordedAnswerer); ok {
				records, err := recorder.RecordedDecisions(ctx, tx, person)
				if err != nil {
					return err
				}
				for _, d := range records {
					d.Ref.Source = name
					decisions = append(decisions, d)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(decisions, func(i, j int) bool {
		a, b := decisions[i], decisions[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.Ref.Source != b.Ref.Source {
			return a.Ref.Source < b.Ref.Source
		}
		return a.Ref.ID < b.Ref.ID
	})
	return decisions, err
}

func cloneSources(sources map[string]Source) map[string]Source {
	copy := make(map[string]Source, len(sources))
	for name, source := range sources {
		copy[name] = source
	}
	return copy
}
