// SPDX-License-Identifier: Apache-2.0
package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// Store owns sender identities, their links and the person's acts on them.
type Store struct {
	db       *canon.Store
	person   string
	people   People
	removals Removals
	now      func() time.Time
}

// Open composes the store. people supplies the people records matching reads;
// removals is the one retraction path a repair travels.
func Open(db *canon.Store, person string, people People, removals Removals) *Store {
	return &Store{db: db, person: person, people: people, removals: removals, now: time.Now}
}

// WithClock replaces the clock for deterministic tests.
func (s *Store) WithClock(now func() time.Time) *Store { s.now = now; return s }

// Person is the owner the store works for.
func (s *Store) Person() string { return s.person }

// Question is the one question the person is asked about an ambiguous identity.
// Candidates are the records offered, by number, in the person's order.
type Question struct {
	Identity   Identity
	Reason     string
	Candidates []string
	AskedAt    time.Time
	State      string
	Answer     string
}

// Requeue is one identity's messages queued for extraction again after a
// repair. It names no message text.
type Requeue struct {
	Identity Identity
	From     time.Time
	Reason   string
	QueuedAt time.Time
}

// Repair is the one-line report of what a link repair removed.
type Repair struct {
	Items       int
	Requeued    bool
	Description string
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, text)
}

type storedIdentifier struct {
	Kind          Kind
	Value, Source string
	First, Last   time.Time
}

// peopleSnapshot reads the known people records once for a matching pass.
func (s *Store) peopleSnapshot(ctx context.Context) ([]Person, error) {
	if s.people == nil || s.person == "" {
		return nil, nil
	}
	records, err := s.people.People(ctx, s.person)
	if err != nil {
		return nil, err
	}
	people := make([]Person, 0, len(records))
	for _, record := range records {
		person := Person{Record: record.Record, Names: append([]string(nil), record.Names...)}
		for _, contact := range record.Identifiers {
			value, err := Normalize(contact.Kind, contact.Value)
			if err != nil {
				// A contact the matcher cannot normalize never links by itself.
				continue
			}
			person.Identifiers = append(person.Identifiers, SeenIdentifier{Kind: contact.Kind, Value: value, Source: contact.Source})
		}
		people = append(people, person)
	}
	return people, nil
}

func normalizeObservation(observation Observation) ([]SeenIdentifier, error) {
	seen := make([]SeenIdentifier, 0, len(observation.Identifiers)+1)
	add := func(identifier SeenIdentifier) error {
		if identifier.Value == "" {
			return nil
		}
		value, err := Normalize(identifier.Kind, identifier.Value)
		if err != nil {
			return err
		}
		identifier.Value = value
		for _, existing := range seen {
			if existing.Kind == identifier.Kind && existing.Value == identifier.Value && existing.Source == identifier.Source {
				return nil
			}
		}
		seen = append(seen, identifier)
		return nil
	}
	for _, identifier := range observation.Identifiers {
		if identifier.Source == "" {
			identifier.Source = "adapter"
		}
		if err := add(identifier); err != nil {
			continue
		}
	}
	if handle, ok := identifierOf(observation.Identity); ok {
		// The handle is the platform's key, so it is also where it was seen.
		_ = add(SeenIdentifier{Kind: handle.Kind, Value: handle.Value, Source: observation.Identity.Platform + " handle", At: observation.At})
	}
	return seen, nil
}

func (s *Store) identifiersTx(ctx context.Context, tx *canon.Tx, identity Identity) ([]storedIdentifier, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind,value,source,first_seen,last_seen FROM identity_identifier
WHERE platform=? AND scope=? AND user=? ORDER BY kind,value,source`, identity.Platform, identity.Scope, identity.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identifiers []storedIdentifier
	for rows.Next() {
		var row storedIdentifier
		var kind, first, last string
		if err := rows.Scan(&kind, &row.Value, &row.Source, &first, &last); err != nil {
			return nil, err
		}
		row.Kind = Kind(kind)
		if row.First, err = parseTime(first); err != nil {
			return nil, err
		}
		if row.Last, err = parseTime(last); err != nil {
			return nil, err
		}
		identifiers = append(identifiers, row)
	}
	return identifiers, rows.Err()
}

func (s *Store) linksTx(ctx context.Context, tx *canon.Tx, identity Identity) ([]Link, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,record,class,maker,created_at,revision,state,valid_from,valid_through
FROM identity_link WHERE platform=? AND scope=? AND user=? ORDER BY created_at,id`, identity.Platform, identity.Scope, identity.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var link Link
		var class, created, state, from, through string
		link.Identity = identity
		if err := rows.Scan(&link.ID, &link.Record, &class, &link.Maker, &created, &link.Revision, &state, &from, &through); err != nil {
			return nil, err
		}
		link.Class, link.State = Class(class), LinkState(state)
		if link.At, err = parseTime(created); err != nil {
			return nil, err
		}
		if link.ValidFrom, err = parseTime(from); err != nil {
			return nil, err
		}
		if link.ValidThrough, err = parseTime(through); err != nil {
			return nil, err
		}
		if link.Signals, err = signalsTx(ctx, tx, link.ID); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func signalsTx(ctx context.Context, tx *canon.Tx, link string) ([]Signal, error) {
	rows, err := tx.QueryContext(ctx, "SELECT kind,source,value FROM identity_link_signal WHERE link=? ORDER BY kind,source,value", link)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var signals []Signal
	for rows.Next() {
		var signal Signal
		if err := rows.Scan(&signal.Kind, &signal.Source, &signal.Value); err != nil {
			return nil, err
		}
		signals = append(signals, signal)
	}
	return signals, rows.Err()
}

// otherIdentities lists the identities other than id that show an identifier.
func (s *Store) otherIdentities(ctx context.Context, tx *canon.Tx, id Identity, kind Kind, value string) ([]Identity, error) {
	rows, err := tx.QueryContext(ctx, `SELECT platform,scope,user FROM identity_identifier
WHERE kind=? AND value=? AND NOT(platform=? AND scope=? AND user=?)`, kind, value, id.Platform, id.Scope, id.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identities []Identity
	for rows.Next() {
		var other Identity
		if err := rows.Scan(&other.Platform, &other.Scope, &other.User); err != nil {
			return nil, err
		}
		identities = append(identities, other)
	}
	return identities, rows.Err()
}

func handleValue(id Identity) string {
	identifier, ok := identifierOf(id)
	if !ok {
		return ""
	}
	return identifier.Value
}

// linkedTo reports whether one of the links points at the record and is usable
// in class. Two identities tied to the same record are the same person.
func linkedTo(links []Link, record string) bool {
	for _, link := range links {
		if link.State == Active && (link.Class == Exact || link.Class == Confirmed) && link.Record == record {
			return true
		}
	}
	return false
}

// samePerson reports whether another identity that shows an identifier is known
// to be the same human as the candidate record. Only an existing link to the
// same record or the same handle as key on both platforms says so; a handle
// claim against an identifier-only link could also be a recycled number.
func samePerson(otherLinks []Link, other, this Identity, value, candidate string) bool {
	bothHandles := handleValue(other) == value && handleValue(this) == value
	for _, link := range otherLinks {
		if link.State != Active || (link.Class != Exact && link.Class != Confirmed) {
			continue
		}
		if candidate == "" || link.Record != candidate {
			continue
		}
		if handleValue(this) == value && handleValue(other) != value {
			return false
		}
		return true
	}
	return bothHandles
}

// Observer is the matching step bound to one people snapshot. Core prepares it
// before a messenger page commits and calls ObserveTx inside that transaction.
type Observer struct {
	store  *Store
	people []Person
	now    time.Time
}

// Prepare reads the people records once for a matching transaction.
func (s *Store) Prepare(ctx context.Context) (*Observer, error) {
	people, err := s.peopleSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &Observer{store: s, people: people, now: s.now()}, nil
}

// Observe records one sender in its own transaction.
func (s *Store) Observe(ctx context.Context, observation Observation) error {
	observer, err := s.Prepare(ctx)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error { return observer.ObserveTx(ctx, tx, observation) })
}

// ObserveTx records one sender, its names and its identifiers, then matches it.
// It writes only inside the caller's transaction.
func (o *Observer) ObserveTx(ctx context.Context, tx *canon.Tx, observation Observation) error {
	if o == nil || o.store == nil || !observation.Identity.valid() {
		return ErrInvalid
	}
	s := o.store
	at := observation.At
	if at.IsZero() {
		at = o.now
	}
	self := 0
	if observation.Self {
		self = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_sender(platform,scope,user,created_at,self) VALUES(?,?,?,?,?)
ON CONFLICT(platform,scope,user) DO UPDATE SET self=max(identity_sender.self,excluded.self)`,
		observation.Identity.Platform, observation.Identity.Scope, observation.Identity.User, formatTime(at), self); err != nil {
		return err
	}
	for _, name := range observation.Names {
		trimmed := strings.TrimSpace(name.Value)
		if trimmed == "" {
			continue
		}
		seenAt := name.At
		if seenAt.IsZero() {
			seenAt = at
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO identity_name(platform,scope,user,name,seen_at) VALUES(?,?,?,?,?)`,
			observation.Identity.Platform, observation.Identity.Scope, observation.Identity.User, trimmed, formatTime(seenAt)); err != nil {
			return err
		}
	}
	identifiers, err := normalizeObservation(observation)
	if err != nil {
		return err
	}
	for _, identifier := range identifiers {
		seenAt := identifier.At
		if seenAt.IsZero() {
			seenAt = at
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_identifier(platform,scope,user,kind,value,source,first_seen,last_seen)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(platform,scope,user,kind,value,source) DO UPDATE SET
 first_seen=CASE WHEN julianday(excluded.first_seen)<julianday(identity_identifier.first_seen)
  THEN excluded.first_seen ELSE identity_identifier.first_seen END,
 last_seen=CASE WHEN julianday(excluded.last_seen)>julianday(identity_identifier.last_seen)
  THEN excluded.last_seen ELSE identity_identifier.last_seen END`,
			observation.Identity.Platform, observation.Identity.Scope, observation.Identity.User, string(identifier.Kind), identifier.Value,
			identifier.Source, formatTime(seenAt), formatTime(seenAt)); err != nil {
			return err
		}
	}
	var storedSelf int
	if err := tx.QueryRowContext(ctx, `SELECT self FROM identity_sender WHERE platform=? AND scope=? AND user=?`,
		observation.Identity.Platform, observation.Identity.Scope, observation.Identity.User).Scan(&storedSelf); err != nil {
		return err
	}
	if storedSelf == 1 {
		// The person's own account is the person, never another person. The
		// flag sticks: a later observation that omits it does not match the
		// account to a people record.
		return nil
	}
	stored, err := s.identifiersTx(ctx, tx, observation.Identity)
	if err != nil {
		return err
	}
	byRecord, err := candidates(o.people, stored)
	if err != nil {
		return err
	}
	links, err := s.linksTx(ctx, tx, observation.Identity)
	if err != nil {
		return err
	}
	reason := ""
	var target string
	var signals []Signal
	var offered []string
	if len(byRecord) > 1 {
		reason = ReasonSharedIdentifier
		offered = candidateRecords(byRecord)
	} else if len(byRecord) == 1 {
		for record, recordSignals := range byRecord {
			target, signals = record, recordSignals
		}
	}
	// An identifier another identity already used either links the same person
	// or is a number that moved. An unresolved reuse is never usable.
	for _, identifier := range stored {
		others, err := s.otherIdentities(ctx, tx, observation.Identity, identifier.Kind, identifier.Value)
		if err != nil {
			return err
		}
		for _, other := range others {
			otherLinks, err := s.linksTx(ctx, tx, other)
			if err != nil {
				return err
			}
			if samePerson(otherLinks, other, observation.Identity, identifier.Value, target) {
				continue
			}
			reason = ReasonIdentifierReused
			target, signals = "", nil
			offered = append(offered, linkRecords(otherLinks)...)
			if err := s.endValidity(ctx, tx, otherLinks, identifier.Kind, identifier.Value, at); err != nil {
				return err
			}
		}
	}
	active := 0
	var activeRecord string
	for _, link := range links {
		if link.State != Active || (link.Class != Exact && link.Class != Confirmed) {
			continue
		}
		active++
		activeRecord = link.Record
	}
	if active > 1 {
		reason = ReasonTwoLinks
		target, signals = "", nil
		offered = append(offered, linkRecords(links)...)
	}
	switch {
	case target != "":
		if activeRecord != "" && activeRecord != target {
			reason = ReasonTwoLinks
			break
		}
		if retired(links, target) {
			break
		}
		if activeRecord != "" {
			if err := s.extendValidity(ctx, tx, links, target, validFrom(stored, signals), at); err != nil {
				return err
			}
			break
		}
		if err := s.insertLink(ctx, tx, observation.Identity, target, Exact, MatcherVersion, at, validFrom(stored, signals), signals); err != nil {
			return err
		}
	case active == 0 && reason == "":
		if err := s.propose(ctx, tx, observation.Identity, o.people, links, at); err != nil {
			return err
		}
	}
	if reason != "" {
		if len(offered) == 0 {
			offered = append(offered, candidateRecords(byRecord)...)
		}
		return s.askOnce(ctx, tx, observation.Identity, reason, at, unique(offered))
	}
	return nil
}

// reasonsFor lists the candidate records of a match, sorted for the person.
func candidateRecords(byRecord map[string][]Signal) []string {
	records := make([]string, 0, len(byRecord))
	for record := range byRecord {
		records = append(records, record)
	}
	sort.Strings(records)
	return records
}

// linkRecords lists the records of active usable-class links.
func linkRecords(links []Link) []string {
	var records []string
	for _, link := range links {
		if link.State == Active && (link.Class == Exact || link.Class == Confirmed) {
			records = append(records, link.Record)
		}
	}
	sort.Strings(records)
	return unique(records)
}

// retired reports whether the person removed or rejected a link to the record
// already. Such a link is kept and never returns.
func retired(links []Link, record string) bool {
	for _, link := range links {
		if link.Record == record && (link.State == Removed || link.State == Rejected) {
			return true
		}
	}
	return false
}

// validFrom is the earliest time an identifier the link rests on was seen.
func validFrom(stored []storedIdentifier, signals []Signal) time.Time {
	var earliest time.Time
	for _, signal := range signals {
		if signal.Kind != string(Phone) && signal.Kind != string(Email) {
			continue
		}
		for _, identifier := range stored {
			if string(identifier.Kind) != signal.Kind || identifier.Value != signal.Value {
				continue
			}
			if earliest.IsZero() || identifier.First.Before(earliest) {
				earliest = identifier.First
			}
		}
	}
	return earliest
}

// extendValidity lowers the link's start to an older sighting of an identifier
// it rests on, so a message from that period is usable. Later sightings never
// shorten a link's validity.
func (s *Store) extendValidity(ctx context.Context, tx *canon.Tx, links []Link, record string, earliest, at time.Time) error {
	if earliest.IsZero() {
		return nil
	}
	for _, link := range links {
		if link.State != Active || link.Record != record || (link.Class != Exact && link.Class != Confirmed) {
			continue
		}
		if !link.ValidFrom.IsZero() && !earliest.Before(link.ValidFrom) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET valid_from=?,revision=revision+1 WHERE id=?", formatTime(earliest), link.ID); err != nil {
			return err
		}
		if err := writeChange(ctx, tx, link.ID, link.Revision+1, "extend", link.Class, Active, record, MatcherVersion, at); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insertLink(ctx context.Context, tx *canon.Tx, id Identity, record string, class Class, maker string, at, from time.Time, signals []Signal) error {
	link := newID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_link(id,platform,scope,user,record,class,maker,created_at,revision,state,valid_from,valid_through)
VALUES(?,?,?,?,?,?,?,?,1,'active',?,?)`, link, id.Platform, id.Scope, id.User, record, string(class), maker, formatTime(at), formatTime(from), ""); err != nil {
		return err
	}
	if err := writeSignals(ctx, tx, link, signals); err != nil {
		return err
	}
	return writeChange(ctx, tx, link, 1, "create", class, Active, record, maker, at)
}

func writeSignals(ctx context.Context, tx *canon.Tx, link string, signals []Signal) error {
	for _, signal := range signals {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO identity_link_signal(link,kind,source,value) VALUES(?,?,?,?)", link, signal.Kind, signal.Source, signal.Value); err != nil {
			return err
		}
	}
	return nil
}

func writeChange(ctx context.Context, tx *canon.Tx, link string, revision int, action string, class Class, state LinkState, record, actor string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_link_change(link,revision,action,class,state,record,actor,at) VALUES(?,?,?,?,?,?,?,?)`,
		link, revision, action, string(class), string(state), record, actor, formatTime(at))
	return err
}

// askOnce records an ambiguity question the first time it is seen, with the
// matches by number. The person is asked once and the answer is kept.
func (s *Store) askOnce(ctx context.Context, tx *canon.Tx, id Identity, reason string, at time.Time, candidates []string) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO identity_question(platform,scope,user,reason,asked_at,state,answer)
VALUES(?,?,?,?,?,'open','') ON CONFLICT(platform,scope,user) DO NOTHING`,
		id.Platform, id.Scope, id.User, reason, formatTime(at))
	if err != nil {
		return err
	}
	if asked, err := result.RowsAffected(); err != nil || asked == 0 {
		return err
	}
	for ordinal, record := range candidates {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO identity_question_candidate(platform,scope,user,ordinal,record) VALUES(?,?,?,?,?)",
			id.Platform, id.Scope, id.User, ordinal, record); err != nil {
			return err
		}
	}
	return nil
}

// propose creates a weak-signal proposal when exactly one record has a similar
// name. Two records with the same name propose nothing.
func (s *Store) propose(ctx context.Context, tx *canon.Tx, id Identity, people []Person, links []Link, at time.Time) error {
	names, err := s.namesTx(ctx, tx, id)
	if err != nil {
		return err
	}
	matched := nameCandidates(people, names)
	if len(matched) != 1 {
		return nil
	}
	for record, signal := range matched {
		if retired(links, record) {
			continue
		}
		for _, link := range links {
			if link.State == Active || (link.State == Removed && link.Record == record) {
				// Already linked, or the person removed this link: no proposal.
				return nil
			}
		}
		return s.insertLink(ctx, tx, id, record, Proposed, MatcherVersion, at, time.Time{}, []Signal{signal})
	}
	return nil
}

func (s *Store) namesTx(ctx context.Context, tx *canon.Tx, id Identity) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM identity_name WHERE platform=? AND scope=? AND user=? ORDER BY seen_at", id.Platform, id.Scope, id.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// endValidity ends the earlier link's validity when an identifier moves to
// another identity.
func (s *Store) endValidity(ctx context.Context, tx *canon.Tx, links []Link, kind Kind, value string, at time.Time) error {
	for _, link := range links {
		if link.State != Active || !link.ValidThrough.IsZero() {
			continue
		}
		used := false
		for _, signal := range link.Signals {
			if signal.Kind == string(kind) && signal.Value == value {
				used = true
				break
			}
		}
		if !used || (!link.ValidFrom.IsZero() && !at.After(link.ValidFrom)) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET valid_through=?,revision=revision+1 WHERE id=?", formatTime(at), link.ID); err != nil {
			return err
		}
		if err := writeChange(ctx, tx, link.ID, link.Revision+1, "moved", link.Class, link.State, link.Record, MatcherVersion, at); err != nil {
			return err
		}
	}
	return nil
}

// assess derives an exact link's ambiguity from every identifier the identity
// currently shows and from its links. A later identifier that belongs to
// another record makes the earlier exact link ambiguous while that conflict is
// open. Confirmed links are the person's word.
func (s *Store) assess(ctx context.Context, tx *canon.Tx, people []Person, link Link) (string, error) {
	if link.State != Active || link.Class == Confirmed || !link.ValidThrough.IsZero() {
		// A link whose identifier moved elsewhere has ended; it is not ambiguous.
		return "", nil
	}
	stored, err := s.identifiersTx(ctx, tx, link.Identity)
	if err != nil {
		return "", err
	}
	byRecord, err := candidates(people, stored)
	if err != nil {
		return "", err
	}
	if len(byRecord) > 1 {
		return ReasonSharedIdentifier, nil
	}
	for record := range byRecord {
		if record != link.Record {
			return ReasonTwoLinks, nil
		}
	}
	links, err := s.linksTx(ctx, tx, link.Identity)
	if err != nil {
		return "", err
	}
	records := map[string]bool{}
	for _, other := range links {
		if other.State == Active && (other.Class == Exact || other.Class == Confirmed) {
			records[other.Record] = true
		}
	}
	if len(records) > 1 {
		return ReasonTwoLinks, nil
	}
	for _, identifier := range stored {
		if identifier.Kind != Phone && identifier.Kind != Email {
			continue
		}
		others, err := s.otherIdentities(ctx, tx, link.Identity, identifier.Kind, identifier.Value)
		if err != nil {
			return "", err
		}
		for _, other := range others {
			otherLinks, err := s.linksTx(ctx, tx, other)
			if err != nil {
				return "", err
			}
			if linkedTo(otherLinks, link.Record) {
				continue
			}
			// Another identity that shows the identifier and is tied to a
			// different record contradicts this link. An identity with no link
			// was already refused one at observation time, so it does not make
			// this link ambiguous.
			for _, otherLink := range otherLinks {
				if otherLink.State == Active && (otherLink.Class == Exact || otherLink.Class == Confirmed) && otherLink.Record != link.Record {
					return ReasonIdentifierConflict, nil
				}
			}
		}
	}
	return "", nil
}

// Links lists an identity's links with the ambiguity of each.
func (s *Store) Links(ctx context.Context, id Identity) ([]Link, error) {
	if !id.valid() {
		return nil, ErrInvalid
	}
	people, err := s.peopleSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	var links []Link
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		links, err = s.linksTx(ctx, tx, id)
		if err != nil {
			return err
		}
		for i := range links {
			reason, err := s.assess(ctx, tx, people, links[i])
			if err != nil {
				return err
			}
			if reason != "" {
				links[i].Ambiguous, links[i].Reason = true, reason
			}
		}
		return nil
	})
	return links, err
}

// Question returns the question asked about an identity, when there is one.
func (s *Store) Question(ctx context.Context, id Identity) (Question, bool, error) {
	var question Question
	found := false
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var asked string
		err := tx.QueryRowContext(ctx, "SELECT reason,asked_at,state,answer FROM identity_question WHERE platform=? AND scope=? AND user=?",
			id.Platform, id.Scope, id.User).Scan(&question.Reason, &asked, &question.State, &question.Answer)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		question.Identity = id
		if question.AskedAt, err = parseTime(asked); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT record FROM identity_question_candidate WHERE platform=? AND scope=? AND user=? ORDER BY ordinal",
			id.Platform, id.Scope, id.User)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record string
			if err := rows.Scan(&record); err != nil {
				return err
			}
			question.Candidates = append(question.Candidates, record)
		}
		found = true
		return rows.Err()
	})
	return question, found, err
}

// Confirm records the person's answer: this identity is this people record.
// A link the answer supersedes is removed, and the items it produced are
// retracted, in that same writer transaction.
func (s *Store) Confirm(ctx context.Context, id Identity, record, words string) (Link, error) {
	if !id.valid() || record == "" {
		return Link{}, ErrInvalid
	}
	at := s.now()
	var confirmed Link
	_, err := s.commitRepair(ctx, func(tx *canon.Tx) ([]string, bool, error) {
		links, err := s.linksTx(ctx, tx, id)
		if err != nil {
			return nil, false, err
		}
		signals := []Signal{{Kind: SignalAnswer, Source: s.person, Value: words}}
		var chosen *Link
		for i := range links {
			if links[i].Record == record && links[i].State != Removed {
				chosen = &links[i]
				break
			}
		}
		if chosen == nil {
			if err := s.insertLink(ctx, tx, id, record, Confirmed, s.person, at, time.Time{}, signals); err != nil {
				return nil, false, err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE identity_link SET class='confirmed',maker=?,revision=revision+1,valid_through='' WHERE id=?`, s.person, chosen.ID); err != nil {
			return nil, false, err
		} else if err := writeChange(ctx, tx, chosen.ID, chosen.Revision+1, "confirm", Confirmed, Active, record, s.person, at); err != nil {
			return nil, false, err
		} else if err := writeSignals(ctx, tx, chosen.ID, signals); err != nil {
			return nil, false, err
		}
		// The person's answer settles the identity: a link the matcher made to
		// another record is superseded and repaired like any wrong match.
		var items []string
		superseded := false
		for _, link := range links {
			if link.State != Active || link.Record == record {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET state='removed',revision=revision+1 WHERE id=?", link.ID); err != nil {
				return nil, false, err
			}
			if err := writeChange(ctx, tx, link.ID, link.Revision+1, "superseded", link.Class, Removed, link.Record, s.person, at); err != nil {
				return nil, false, err
			}
			cited, err := s.citingItems(ctx, tx, link.ID)
			if err != nil {
				return nil, false, err
			}
			if err := dropItemArtifacts(ctx, tx, link.ID, cited); err != nil {
				return nil, false, err
			}
			items = append(items, cited...)
			superseded = true
		}
		requeued := false
		if superseded {
			var err error
			if requeued, err = s.requeueTx(ctx, tx, id, at, "link superseded"); err != nil {
				return nil, false, err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_question SET state='answered',answer=? WHERE platform=? AND scope=? AND user=?", words, id.Platform, id.Scope, id.User); err != nil {
			return nil, false, err
		}
		links, err = s.linksTx(ctx, tx, id)
		if err != nil {
			return nil, false, err
		}
		confirmed = Link{}
		for _, link := range links {
			if link.Record == record && link.State == Active {
				confirmed = link
			}
		}
		if confirmed.ID == "" {
			return nil, false, ErrNotFound
		}
		return items, requeued, nil
	})
	if err != nil {
		return Link{}, err
	}
	return confirmed, nil
}

// Reject keeps the person's refusal: the link is no longer usable and never
// re-proposed.
func (s *Store) Reject(ctx context.Context, linkID string) error {
	return s.setState(ctx, linkID, Rejected, "reject")
}

func (s *Store) setState(ctx context.Context, linkID string, state LinkState, action string) error {
	if linkID == "" {
		return ErrInvalid
	}
	at := s.now()
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var identity Identity
		var revision int
		var class Class
		var record string
		if err := tx.QueryRowContext(ctx, "SELECT platform,scope,user,revision,class,record FROM identity_link WHERE id=?", linkID).
			Scan(&identity.Platform, &identity.Scope, &identity.User, &revision, &class, &record); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET state=?,revision=revision+1 WHERE id=?", string(state), linkID); err != nil {
			return err
		}
		return writeChange(ctx, tx, linkID, revision+1, action, class, state, record, s.person, at)
	})
}

// Remove is the person's act: the link stops being usable, every item it
// produced is retracted through the one retraction path, and only this
// identity's messages after the learning watermark are queued again. The link
// change and the retraction commit together.
func (s *Store) Remove(ctx context.Context, linkID string) (Repair, error) {
	if linkID == "" {
		return Repair{}, ErrInvalid
	}
	at := s.now()
	return s.commitRepair(ctx, func(tx *canon.Tx) ([]string, bool, error) {
		var identity Identity
		var revision int
		var class Class
		var record string
		if err := tx.QueryRowContext(ctx, "SELECT platform,scope,user,revision,class,record FROM identity_link WHERE id=?", linkID).
			Scan(&identity.Platform, &identity.Scope, &identity.User, &revision, &class, &record); err != nil {
			return nil, false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET state='removed',revision=revision+1 WHERE id=?", linkID); err != nil {
			return nil, false, err
		}
		if err := writeChange(ctx, tx, linkID, revision+1, "remove", class, Removed, record, s.person, at); err != nil {
			return nil, false, err
		}
		items, err := s.citingItems(ctx, tx, linkID)
		if err != nil {
			return nil, false, err
		}
		if err := dropItemArtifacts(ctx, tx, linkID, items); err != nil {
			return nil, false, err
		}
		requeued, err := s.requeueTx(ctx, tx, identity, at, "link removed")
		return items, requeued, err
	})
}

// Correct moves an identity to the right people record. The items the old link
// produced are retracted, and only this identity's messages after the learning
// watermark are queued again. The move and the retraction commit together.
func (s *Store) Correct(ctx context.Context, linkID, record string) (Repair, error) {
	if linkID == "" || record == "" {
		return Repair{}, ErrInvalid
	}
	at := s.now()
	return s.commitRepair(ctx, func(tx *canon.Tx) ([]string, bool, error) {
		var identity Identity
		var revision int
		var class Class
		var previous string
		if err := tx.QueryRowContext(ctx, "SELECT platform,scope,user,revision,class,record FROM identity_link WHERE id=?", linkID).
			Scan(&identity.Platform, &identity.Scope, &identity.User, &revision, &class, &previous); err != nil {
			return nil, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identity_link SET record=?,class='confirmed',maker=?,state='active',revision=revision+1,valid_through='' WHERE id=?`,
			record, s.person, linkID); err != nil {
			return nil, false, err
		}
		if err := writeChange(ctx, tx, linkID, revision+1, "correct", Confirmed, Active, record, s.person, at); err != nil {
			return nil, false, err
		}
		answer := []Signal{{Kind: SignalAnswer, Source: s.person, Value: "moved to " + record}}
		if err := writeSignals(ctx, tx, linkID, answer); err != nil {
			return nil, false, err
		}
		items, err := s.citingItems(ctx, tx, linkID)
		if err != nil {
			return nil, false, err
		}
		if err := dropItemArtifacts(ctx, tx, linkID, items); err != nil {
			return nil, false, err
		}
		requeued, err := s.requeueTx(ctx, tx, identity, at, "link corrected")
		return items, requeued, err
	})
}

// citingItems lists the memory items a link produced. The artifact table is the
// record of what the link wrote; a citation of the link itself counts too.
// An item hidden only because an ancestor was forgotten is included: restoring
// that ancestor would show it again unless this repair retracts it. An item
// that is already retracted, or whose row is gone, is left as it is.
func (s *Store) citingItems(ctx context.Context, tx *canon.Tx, linkID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM identity_artifact WHERE link=? AND kind='item'
UNION
SELECT view_id FROM cite WHERE view_kind='memory' AND target_kind='external' AND target_id=? AND dropped_revision IS NULL`,
		linkID, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		var repairable bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM memory_item m
WHERE m.id=? AND m.person=? AND m.state='active'
AND NOT EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=m.id AND r.state<>'active'))`,
			id, s.person).Scan(&repairable); err != nil {
			return nil, err
		}
		if repairable {
			items = append(items, id)
		}
	}
	return items, rows.Err()
}

// producedItems lists every memory item a link produced that still has a row,
// including one the person has forgotten. Split moves these with the identity.
// A permanently deleted item has no row and is absent. citingItems is the
// narrower list a repair retracts.
func (s *Store) producedItems(ctx context.Context, tx *canon.Tx, linkID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM identity_artifact WHERE link=? AND kind='item'
UNION
SELECT view_id FROM cite WHERE view_kind='memory' AND target_kind='external' AND target_id=? AND dropped_revision IS NULL`,
		linkID, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		var present bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM memory_item m WHERE m.id=? AND m.person=?)`, id, s.person).Scan(&present); err != nil {
			return nil, err
		}
		if present {
			items = append(items, id)
		}
	}
	return items, rows.Err()
}

// atomicRemovals journals a forget in the same fenced writer as the caller's
// canon changes. The removal list Core opens implements it.
type atomicRemovals interface {
	ForgetDuring(context.Context, string, func(*canon.Tx) ([]retract.Ref, error)) error
}

// commitRepair runs mutate and the retraction of the items it names in one
// writer transaction. An empty item list commits mutate and writes no removal
// line. A removal path that cannot share that writer leaves the link unchanged
// when there are items to retract.
func (s *Store) commitRepair(ctx context.Context, mutate func(*canon.Tx) ([]string, bool, error)) (Repair, error) {
	describe := func(n int, requeued bool) Repair {
		repair := Repair{Items: n, Requeued: requeued, Description: fmt.Sprintf("%d items retracted", n)}
		if requeued {
			repair.Description += "; messages requeued after the learning watermark"
		}
		return repair
	}
	during, ok := s.removals.(atomicRemovals)
	if !ok {
		var repair Repair
		err := s.db.Write(ctx, func(tx *canon.Tx) error {
			items, requeued, err := mutate(tx)
			if err != nil {
				return err
			}
			if len(items) > 0 {
				return ErrInvalid
			}
			repair = describe(0, requeued)
			return nil
		})
		if err != nil {
			return Repair{}, err
		}
		return repair, nil
	}
	var repair Repair
	err := during.ForgetDuring(ctx, s.person, func(tx *canon.Tx) ([]retract.Ref, error) {
		items, requeued, err := mutate(tx)
		if err != nil {
			return nil, err
		}
		refs := make([]retract.Ref, 0, len(items))
		seen := map[string]bool{}
		for _, item := range items {
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			refs = append(refs, retract.Ref{Kind: retract.Memory, ID: item})
		}
		repair = describe(len(refs), requeued)
		return refs, nil
	})
	if err != nil {
		return Repair{}, err
	}
	return repair, nil
}

// dropItemArtifacts removes the item rows a repair is retracting. A
// people-record entry is retracted by the write that owns it (#1077).
func dropItemArtifacts(ctx context.Context, tx *canon.Tx, linkID string, items []string) error {
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "DELETE FROM identity_artifact WHERE link=? AND kind='item' AND id=?", linkID, item); err != nil {
			return err
		}
	}
	return nil
}

// requeueTx queues only this identity's messages after the source's learning
// watermark. Nothing else is queued.
func (s *Store) requeueTx(ctx context.Context, tx *canon.Tx, id Identity, at time.Time, reason string) (bool, error) {
	from := ""
	if err := tx.QueryRowContext(ctx, `SELECT learning_watermark FROM messenger_source
WHERE platform=? AND (workspace=? OR workspace='') AND learning_watermark<>'' ORDER BY connected_at LIMIT 1`,
		id.Platform, id.Scope).Scan(&from); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	fromAt, err := parseTime(from)
	if err != nil {
		return false, err
	}
	if fromAt.IsZero() {
		var first string
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(min(first_seen),'') FROM identity_identifier WHERE platform=? AND scope=? AND user=?`,
			id.Platform, id.Scope, id.User).Scan(&first); err != nil {
			return false, err
		}
		if fromAt, err = parseTime(first); err != nil {
			return false, err
		}
	}
	if fromAt.IsZero() {
		fromAt = at
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_requeue(platform,scope,user,from_at,reason,queued_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(platform,scope,user,from_at) DO NOTHING`,
		id.Platform, id.Scope, id.User, formatTime(fromAt), reason, formatTime(at)); err != nil {
		return false, err
	}
	return true, nil
}

// Requeues lists the identities waiting for extraction again.
func (s *Store) Requeues(ctx context.Context) ([]Requeue, error) {
	var requeues []Requeue
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT platform,scope,user,from_at,reason,queued_at FROM identity_requeue ORDER BY queued_at,platform,scope,user")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var requeue Requeue
			var from, queued string
			if err := rows.Scan(&requeue.Identity.Platform, &requeue.Identity.Scope, &requeue.Identity.User, &from, &requeue.Reason, &queued); err != nil {
				return err
			}
			if requeue.From, err = parseTime(from); err != nil {
				return err
			}
			if requeue.QueuedAt, err = parseTime(queued); err != nil {
				return err
			}
			requeues = append(requeues, requeue)
		}
		return rows.Err()
	})
	return requeues, err
}

// Merge records that two people records are one human. It is the person's act,
// so their answer is the evidence a merge rests on; both record ids are kept,
// the aliases of both are kept with the merge, the links and items of the first
// record move to the second, and the merge can be undone.
func (s *Store) Merge(ctx context.Context, from, into string) (string, error) {
	if from == "" || into == "" || from == into {
		return "", ErrInvalid
	}
	people, err := s.peopleSnapshot(ctx)
	if err != nil {
		return "", err
	}
	at := s.now()
	merge := newID()
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO identity_merge(id,person,from_record,into_record,state,at,undone_at) VALUES(?,?,?,?,'merged',?,'')",
			merge, s.person, from, into, formatTime(at)); err != nil {
			return err
		}
		for _, person := range people {
			if person.Record != from && person.Record != into {
				continue
			}
			for _, name := range person.Names {
				if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO identity_merge_alias(merge,record,name) VALUES(?,?,?)", merge, person.Record, name); err != nil {
					return err
				}
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,record,revision,class FROM identity_link WHERE record=? AND state='active'`, from)
		if err != nil {
			return err
		}
		type linkRow struct {
			id, record string
			revision   int
			class      Class
		}
		var links []linkRow
		for rows.Next() {
			var row linkRow
			var class string
			if err := rows.Scan(&row.id, &row.record, &row.revision, &class); err != nil {
				rows.Close()
				return err
			}
			row.class = Class(class)
			links = append(links, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, link := range links {
			if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET record=?,revision=revision+1 WHERE id=?", into, link.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO identity_merge_link(merge,link,record) VALUES(?,?,?)", merge, link.id, from); err != nil {
				return err
			}
			if err := writeChange(ctx, tx, link.id, link.revision+1, "merge", link.class, Active, into, s.person, at); err != nil {
				return err
			}
		}
		items, err := s.subjectItems(ctx, tx, from)
		if err != nil {
			return err
		}
		for _, item := range items {
			if _, err := memory.ReassignSubject(ctx, tx, s.person, item, from, into); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO identity_merge_artifact(merge,item,record) VALUES(?,?,?)", merge, item, from); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return merge, nil
}

func (s *Store) subjectItems(ctx context.Context, tx *canon.Tx, record string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM memory_item WHERE person=? AND subject_kind='third_party' AND subject_id=? AND state='active' ORDER BY id`, s.person, record)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	return items, rows.Err()
}

// MergeAliases returns the aliases the merged records carried, by record. The
// people-record writer carries them onto the surviving record.
func (s *Store) MergeAliases(ctx context.Context, merge string) (map[string][]string, error) {
	aliases := map[string][]string{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record,name FROM identity_merge_alias WHERE merge=? ORDER BY record,name", merge)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record, name string
			if err := rows.Scan(&record, &name); err != nil {
				return err
			}
			aliases[record] = append(aliases[record], name)
		}
		return rows.Err()
	})
	return aliases, err
}

// UndoMerge puts every link and every item that still exists back on the
// record it came from. A forgotten item moves back still forgotten. A
// permanently deleted item stays gone, and neither blocks the undo.
func (s *Store) UndoMerge(ctx context.Context, merge string) error {
	if merge == "" {
		return ErrInvalid
	}
	at := s.now()
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var state, from, into string
		if err := tx.QueryRowContext(ctx, "SELECT state,from_record,into_record FROM identity_merge WHERE id=? AND person=?", merge, s.person).Scan(&state, &from, &into); err != nil {
			return err
		}
		if state != "merged" {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_merge SET state='undone',undone_at=? WHERE id=?", formatTime(at), merge); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT link,record FROM identity_merge_link WHERE merge=? ORDER BY link", merge)
		if err != nil {
			return err
		}
		type linkRow struct{ link, record string }
		var links []linkRow
		for rows.Next() {
			var row linkRow
			if err := rows.Scan(&row.link, &row.record); err != nil {
				rows.Close()
				return err
			}
			links = append(links, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, link := range links {
			var revision int
			var class Class
			if err := tx.QueryRowContext(ctx, "SELECT revision,class FROM identity_link WHERE id=?", link.link).Scan(&revision, &class); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET record=?,revision=revision+1 WHERE id=?", link.record, link.link); err != nil {
				return err
			}
			if err := writeChange(ctx, tx, link.link, revision+1, "undo_merge", class, Active, link.record, s.person, at); err != nil {
				return err
			}
		}
		itemRows, err := tx.QueryContext(ctx, "SELECT item,record FROM identity_merge_artifact WHERE merge=? ORDER BY item", merge)
		if err != nil {
			return err
		}
		type itemRow struct{ item, record string }
		var items []itemRow
		for itemRows.Next() {
			var row itemRow
			if err := itemRows.Scan(&row.item, &row.record); err != nil {
				itemRows.Close()
				return err
			}
			items = append(items, row)
		}
		if err := errors.Join(itemRows.Err(), itemRows.Close()); err != nil {
			return err
		}
		for _, item := range items {
			if _, err := memory.ReassignSubject(ctx, tx, s.person, item.item, into, item.record); err != nil {
				if errors.Is(err, memory.ErrNotFound) {
					continue
				}
				return err
			}
		}
		return nil
	})
}

// Split is the person's act: the listed identities and the items their evidence
// produced move from one record to another. A forgotten item moves still
// forgotten. A permanently deleted item stays gone, and neither blocks the split.
func (s *Store) Split(ctx context.Context, from, into string, identities []Identity) error {
	if from == "" || into == "" || from == into || len(identities) == 0 {
		return ErrInvalid
	}
	at := s.now()
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, id := range identities {
			if !id.valid() {
				return ErrInvalid
			}
			rows, err := tx.QueryContext(ctx, `SELECT id,revision,class FROM identity_link WHERE platform=? AND scope=? AND user=? AND record=? AND state='active'`,
				id.Platform, id.Scope, id.User, from)
			if err != nil {
				return err
			}
			type linkRow struct {
				id       string
				revision int
				class    Class
			}
			var links []linkRow
			for rows.Next() {
				var row linkRow
				var class string
				if err := rows.Scan(&row.id, &row.revision, &class); err != nil {
					rows.Close()
					return err
				}
				row.class = Class(class)
				links = append(links, row)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			for _, link := range links {
				if _, err := tx.ExecContext(ctx, "UPDATE identity_link SET record=?,revision=revision+1 WHERE id=?", into, link.id); err != nil {
					return err
				}
				if err := writeChange(ctx, tx, link.id, link.revision+1, "split", link.class, Active, into, s.person, at); err != nil {
					return err
				}
				items, err := s.producedItems(ctx, tx, link.id)
				if err != nil {
					return err
				}
				for _, item := range items {
					if _, err := memory.ReassignSubject(ctx, tx, s.person, item, from, into); err != nil {
						if errors.Is(err, memory.ErrNotFound) {
							continue
						}
						return err
					}
				}
			}
		}
		return nil
	})
}

// RecordArtifact records that a link produced a memory item or a people-record
// entry, so a repair can retract it. Learning about matched senders calls it.
func (s *Store) RecordArtifact(ctx context.Context, linkID, kind, id string) error {
	if linkID == "" || id == "" || (kind != "item" && kind != "record_entry") {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO identity_artifact(link,kind,id) VALUES(?,?,?)", linkID, kind, id)
		return err
	})
}

// Proposals lists the weak-signal proposals waiting for the person, at most
// one batch. Batches hold three to five; a smaller remainder is a batch too.
func (s *Store) Proposals(ctx context.Context, limit int) ([]Link, error) {
	if limit <= 0 || limit > 5 {
		limit = 5
	}
	var links []Link
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT platform,scope,user FROM identity_link
WHERE state='active' AND class='proposed' ORDER BY created_at,id`)
		if err != nil {
			return err
		}
		var identities []Identity
		for rows.Next() {
			var id Identity
			if err := rows.Scan(&id.Platform, &id.Scope, &id.User); err != nil {
				rows.Close()
				return err
			}
			identities = append(identities, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range identities {
			if len(links) >= limit {
				return nil
			}
			linksFor, err := s.linksTx(ctx, tx, id)
			if err != nil {
				return err
			}
			for _, link := range linksFor {
				if link.Class == Proposed && link.State == Active && len(links) < limit {
					links = append(links, link)
				}
			}
		}
		return nil
	})
	return links, err
}

// Rematch matches every known identity that has no active link again, so a
// people record the person creates after the messages arrive still links. The
// messenger loop calls it on its schedule, and Core calls it once at start.
func (s *Store) Rematch(ctx context.Context) (int, error) {
	observer, err := s.Prepare(ctx)
	if err != nil {
		return 0, err
	}
	var identities []Identity
	var selves map[string]bool = map[string]bool{}
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT platform,scope,user,self FROM identity_sender s
WHERE self=0 AND NOT EXISTS(SELECT 1 FROM identity_link l WHERE l.platform=s.platform AND l.scope=s.scope AND l.user=s.user AND l.state='active')
ORDER BY platform,scope,user`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id Identity
			var self int
			if err := rows.Scan(&id.Platform, &id.Scope, &id.User, &self); err != nil {
				return err
			}
			identities = append(identities, id)
			if self == 1 {
				selves[id.Key()] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	matched := 0
	for _, id := range identities {
		err := s.db.Write(ctx, func(tx *canon.Tx) error {
			names, err := sqlNames(ctx, tx, id)
			if err != nil {
				return err
			}
			identifiers, err := s.identifiersTx(ctx, tx, id)
			if err != nil {
				return err
			}
			observation := Observation{Identity: id, Self: selves[id.Key()], Names: names}
			for _, identifier := range identifiers {
				observation.Identifiers = append(observation.Identifiers, SeenIdentifier{Kind: identifier.Kind, Value: identifier.Value, Source: identifier.Source, At: identifier.Last})
			}
			return observer.ObserveTx(ctx, tx, observation)
		})
		if err != nil {
			return matched, err
		}
		links, err := s.Links(ctx, id)
		if err != nil {
			return matched, err
		}
		for _, link := range links {
			if link.Class == Exact && link.State == Active && !link.Ambiguous {
				matched++
				break
			}
		}
	}
	return matched, nil
}

func sqlNames(ctx context.Context, tx *canon.Tx, id Identity) ([]NameSeen, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name,seen_at FROM identity_name WHERE platform=? AND scope=? AND user=? ORDER BY seen_at", id.Platform, id.Scope, id.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []NameSeen
	for rows.Next() {
		var name, seen string
		if err := rows.Scan(&name, &seen); err != nil {
			return nil, err
		}
		seenAt, err := parseTime(seen)
		if err != nil {
			return nil, err
		}
		names = append(names, NameSeen{Value: name, At: seenAt})
	}
	return names, rows.Err()
}
