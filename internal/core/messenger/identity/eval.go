// SPDX-License-Identifier: Apache-2.0
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
)

// Set is the labelled evaluation input: known people, the identities observed,
// and the ambiguities planted on purpose. A label of "unknown" means no usable
// link is correct; "self" is the person's own account, which is never linked.
type Set struct {
	Version     int            `json:"version"`
	People      []SetPerson    `json:"people"`
	Identities  []SetIdentity  `json:"identities"`
	Ambiguities []SetAmbiguity `json:"ambiguities,omitempty"`
	Windows     []SetWindow    `json:"windows,omitempty"`
}

// SetPerson is one people record in a set.
type SetPerson struct {
	ID       string       `json:"id"`
	Names    []string     `json:"names"`
	Contacts []SetContact `json:"contacts,omitempty"`
}

// SetContact is one identifier with where it was seen.
type SetContact struct {
	Kind   Kind   `json:"kind"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// SetIdentity is one sender identity as a source saw it.
type SetIdentity struct {
	Platform    string       `json:"platform"`
	Scope       string       `json:"scope,omitempty"`
	User        string       `json:"user"`
	Self        bool         `json:"self,omitempty"`
	At          string       `json:"at"`
	ObservedAt  string       `json:"observed_at,omitempty"`
	Names       []string     `json:"names,omitempty"`
	Identifiers []SetContact `json:"identifiers,omitempty"`
	Expect      string       `json:"expect"`
}

// SetWindow is a period in which a link to a record must not be usable: the
// evidence of the identifier's move arrived out of order, and the link's end
// must be the earliest move, not the sighting that arrived first.
type SetWindow struct {
	Platform string `json:"platform"`
	Scope    string `json:"scope,omitempty"`
	User     string `json:"user"`
	Record   string `json:"record"`
	From     string `json:"from"`
	Until    string `json:"until"`
	Why      string `json:"why,omitempty"`
}

// SetAmbiguity is one ambiguity the set plants: this identity must be flagged
// and must not become usable.
type SetAmbiguity struct {
	Platform string `json:"platform"`
	Scope    string `json:"scope,omitempty"`
	User     string `json:"user"`
	Reason   string `json:"reason,omitempty"`
}

// Identity returns the ambiguity's identity key.
func (a SetAmbiguity) Identity() Identity {
	return Identity{Platform: a.Platform, Scope: a.Scope, User: a.User}
}

// Measures reports what the matcher did on a set. Rates are over usable links
// only: unmatched, ambiguous and unknown identities say nothing about link
// correctness.
type Measures struct {
	Identities          int
	UsableLinks         int
	WrongLinks          int
	LinkedAutomatically int
	Ambiguities         int
	AmbiguitiesFlagged  int
	Proposals           int
	ProposalTop1        int
	Windows             int
	WindowsRejected     int
	Repair              RepairMeasure
}

// RepairMeasure reports the repair check: no item cites a removed link and only
// the repaired identity's messages are queued again.
type RepairMeasure struct {
	Link           string
	ItemsRetracted int
	Citing         int
	Requeued       int
	OtherRequeued  int
	Clean          bool
}

// Precision is the share of usable links that point at the right record.
func (m Measures) Precision() float64 {
	if m.UsableLinks == 0 {
		return 1
	}
	return float64(m.UsableLinks-m.WrongLinks) / float64(m.UsableLinks)
}

// LinkedShare is the share of identities the matcher linked on its own.
func (m Measures) LinkedShare() float64 {
	if m.Identities == 0 {
		return 0
	}
	return float64(m.LinkedAutomatically) / float64(m.Identities)
}

// ProposalTop1Rate is the share of proposals that named the right record.
func (m Measures) ProposalTop1Rate() float64 {
	if m.Proposals == 0 {
		return 1
	}
	return float64(m.ProposalTop1) / float64(m.Proposals)
}

// WrongRateBound is the rule-of-three bound 3/n over the usable links checked.
func (m Measures) WrongRateBound() float64 {
	if m.UsableLinks == 0 {
		return 1
	}
	return 3 / float64(m.UsableLinks)
}

// Gate reports whether the matcher may be used for learning: no wrong usable
// link, every planted ambiguity flagged, and every moved-identifier window
// rejected.
func (m Measures) Gate() bool {
	return m.WrongLinks == 0 && m.AmbiguitiesFlagged == m.Ambiguities && m.WindowsRejected == m.Windows
}

// LoadSet reads an evaluation set.
func LoadSet(path string) (Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Set{}, err
	}
	return ParseSet(data)
}

// ParseSet decodes an evaluation set and checks its shape.
func ParseSet(data []byte) (Set, error) {
	var set Set
	if err := json.Unmarshal(data, &set); err != nil {
		return Set{}, err
	}
	if set.Version != 1 || len(set.Identities) == 0 {
		return Set{}, ErrInvalid
	}
	for _, person := range set.People {
		if person.ID == "" {
			return Set{}, ErrInvalid
		}
	}
	for _, identity := range set.Identities {
		if !(Identity{Platform: identity.Platform, Scope: identity.Scope, User: identity.User}).valid() || identity.Expect == "" {
			return Set{}, ErrInvalid
		}
		if _, err := time.Parse(time.RFC3339, identity.At); err != nil {
			return Set{}, ErrInvalid
		}
		if identity.ObservedAt != "" {
			if _, err := time.Parse(time.RFC3339, identity.ObservedAt); err != nil {
				return Set{}, ErrInvalid
			}
		}
		for _, contact := range identity.Identifiers {
			if _, err := Normalize(contact.Kind, contact.Value); err != nil {
				return Set{}, fmt.Errorf("%w: %s %q", ErrInvalid, contact.Kind, contact.Value)
			}
		}
	}
	for _, window := range set.Windows {
		if !(Identity{Platform: window.Platform, Scope: window.Scope, User: window.User}).valid() || window.Record == "" {
			return Set{}, ErrInvalid
		}
		from, err := time.Parse(time.RFC3339, window.From)
		if err != nil {
			return Set{}, ErrInvalid
		}
		until, err := time.Parse(time.RFC3339, window.Until)
		if err != nil || !until.After(from) {
			return Set{}, ErrInvalid
		}
	}
	return set, nil
}

// StaticPeople is a people snapshot from a set. It is also what the private run
// reads in place of the person's records.
type StaticPeople []Person

// People implements People.
func (p StaticPeople) People(context.Context, string) ([]Person, error) { return p, nil }

// SetPeople builds the people snapshot a set describes.
func SetPeople(set Set) (StaticPeople, error) {
	people := make(StaticPeople, 0, len(set.People))
	for _, record := range set.People {
		person := Person{Record: record.ID, Names: append([]string(nil), record.Names...)}
		for _, contact := range record.Contacts {
			value, err := Normalize(contact.Kind, contact.Value)
			if err != nil {
				return nil, err
			}
			person.Identifiers = append(person.Identifiers, SeenIdentifier{Kind: contact.Kind, Value: value, Source: contact.Source})
		}
		people = append(people, person)
	}
	return people, nil
}

// WithPeople replaces the people source for a run.
func (s *Store) WithPeople(people People) *Store { s.people = people; return s }

// Evaluate runs the set through the store and reports the measures. The store's
// own people source is replaced by the set's people records.
func Evaluate(ctx context.Context, store *Store, set Set) (Measures, error) {
	people, err := SetPeople(set)
	if err != nil {
		return Measures{}, err
	}
	run := store.WithPeople(people)
	identities := append([]SetIdentity(nil), set.Identities...)
	// Evidence can arrive out of order: a backfill is observed after a later
	// sighting. ObservedAt is the arrival order; At stays the sighting time.
	arrival := func(identity SetIdentity) string {
		if identity.ObservedAt != "" {
			return identity.ObservedAt
		}
		return identity.At
	}
	sort.SliceStable(identities, func(i, j int) bool { return arrival(identities[i]) < arrival(identities[j]) })
	var latest time.Time
	for _, identity := range identities {
		at, err := time.Parse(time.RFC3339, identity.At)
		if err != nil {
			return Measures{}, err
		}
		if at.After(latest) {
			latest = at
		}
		if seen, err := time.Parse(time.RFC3339, arrival(identity)); err == nil && seen.After(latest) {
			latest = seen
		}
		observation := Observation{
			Identity: Identity{Platform: identity.Platform, Scope: identity.Scope, User: identity.User},
			Self:     identity.Self,
			At:       at,
		}
		for _, name := range identity.Names {
			observation.Names = append(observation.Names, NameSeen{Value: name, At: at})
		}
		for _, identifier := range identity.Identifiers {
			observation.Identifiers = append(observation.Identifiers, SeenIdentifier{Kind: identifier.Kind, Value: identifier.Value, Source: identifier.Source, At: at})
		}
		if err := run.Observe(ctx, observation); err != nil {
			return Measures{}, err
		}
	}
	now := latest.Add(time.Second)
	measures := Measures{Identities: len(identities), Ambiguities: len(set.Ambiguities), Windows: len(set.Windows)}
	for _, identity := range identities {
		id := Identity{Platform: identity.Platform, Scope: identity.Scope, User: identity.User}
		links, err := run.Links(ctx, id)
		if err != nil {
			return Measures{}, err
		}
		linked := false
		for _, link := range links {
			if !link.Usable(now) {
				continue
			}
			measures.UsableLinks++
			if identity.Expect == "unknown" || identity.Expect == "self" || link.Record != identity.Expect {
				measures.WrongLinks++
			}
			if link.Maker == MatcherVersion {
				linked = true
			}
		}
		if linked {
			measures.LinkedAutomatically++
		}
		for _, link := range links {
			if link.State == Active && link.Class == Proposed {
				measures.Proposals++
				if link.Record == identity.Expect {
					measures.ProposalTop1++
				}
			}
		}
	}
	for _, ambiguity := range set.Ambiguities {
		flagged, err := ambiguityFlagged(ctx, run, ambiguity, now)
		if err != nil {
			return Measures{}, err
		}
		if flagged {
			measures.AmbiguitiesFlagged++
		}
	}
	for _, window := range set.Windows {
		rejected, err := windowRejected(ctx, run, window)
		if err != nil {
			return Measures{}, err
		}
		if rejected {
			measures.WindowsRejected++
		}
	}
	repair, err := measureRepair(ctx, run, set, now)
	if err != nil {
		return Measures{}, err
	}
	measures.Repair = repair
	return measures, nil
}

// windowRejected reports whether a link to the window's record is unusable at
// every moment of the window, including its end.
func windowRejected(ctx context.Context, store *Store, window SetWindow) (bool, error) {
	from, err := time.Parse(time.RFC3339, window.From)
	if err != nil {
		return false, err
	}
	until, err := time.Parse(time.RFC3339, window.Until)
	if err != nil {
		return false, err
	}
	links, err := store.Links(ctx, Identity{Platform: window.Platform, Scope: window.Scope, User: window.User})
	if err != nil {
		return false, err
	}
	for _, moment := range []time.Time{from, from.Add(until.Sub(from) / 2), until} {
		for _, link := range links {
			if link.Record == window.Record && link.Usable(moment) {
				return false, nil
			}
		}
	}
	return true, nil
}

func ambiguityFlagged(ctx context.Context, store *Store, ambiguity SetAmbiguity, now time.Time) (bool, error) {
	links, err := store.Links(ctx, ambiguity.Identity())
	if err != nil {
		return false, err
	}
	for _, link := range links {
		if link.Ambiguous && link.Usable(now) {
			return false, nil
		}
		if link.State == Active && link.Usable(now) {
			return false, nil
		}
	}
	if len(links) == 0 {
		// No link was made at all; the question is the flag.
		question, found, err := store.Question(ctx, ambiguity.Identity())
		if err != nil {
			return false, err
		}
		return found && question.State == "open", nil
	}
	return true, nil
}

// measureRepair checks the repair path on one usable link: an item it produced
// is retracted through the removal list, and only that identity is queued.
func measureRepair(ctx context.Context, store *Store, set Set, now time.Time) (RepairMeasure, error) {
	var repair RepairMeasure
	var chosen Link
	var chosenSet *SetIdentity
	for i := range set.Identities {
		identity := set.Identities[i]
		if identity.Expect == "unknown" || identity.Expect == "self" {
			continue
		}
		links, err := store.Links(ctx, Identity{Platform: identity.Platform, Scope: identity.Scope, User: identity.User})
		if err != nil {
			return repair, err
		}
		for _, link := range links {
			if link.Usable(now) && link.Record == identity.Expect {
				chosen, chosenSet = link, &set.Identities[i]
				break
			}
		}
		if chosen.ID != "" {
			break
		}
	}
	if chosen.ID == "" {
		return repair, nil
	}
	repair.Link = chosen.ID
	at := now.Add(-time.Hour)
	event := "identity-repair-" + chosen.ID
	_, err := ledger.New(store.db, nil).Admit(ctx, guard.EntryOwner, ledger.Input{
		ID: event, ConversationID: "identity:repair", AgentID: "agent", PersonID: store.person,
		Surface:    ledger.Surface{Kind: "tui"},
		OccurredAt: at, RecordedAt: at, TimeQuality: "recorded", Body: "synthetic record", Locator: event,
	})
	if err != nil {
		return repair, err
	}
	item := memory.Item{
		Kind: memory.Fact, Content: "synthetic fact", Person: store.person,
		Subject: memory.Subject{Kind: memory.ThirdParty, ID: chosen.Record}, Basis: memory.Derived,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: event, Precision: memory.Exact}},
	}
	var stored memory.Item
	if err := store.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		stored, err = memory.Insert(ctx, tx, memory.Write{Agent: "agent", Person: store.person, Item: item}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
			return memory.Origin{Person: store.person, Agent: "agent", Speaker: guard.Person, Scope: memory.Private, Quote: "synthetic record"}, nil
		})
		return err
	}); err != nil {
		return repair, err
	}
	if err := store.RecordArtifact(ctx, chosen.ID, "item", stored.ID); err != nil {
		return repair, err
	}
	before, err := store.Requeues(ctx)
	if err != nil {
		return repair, err
	}
	result, err := store.Remove(ctx, chosen.ID)
	if err != nil {
		return repair, err
	}
	repair.ItemsRetracted = result.Items
	repair.Citing = 1
	requeues, err := store.Requeues(ctx)
	if err != nil {
		return repair, err
	}
	// Only the rows this repair queued count; a repair earlier in the run may
	// have left its own.
	seen := map[string]bool{}
	for _, requeue := range before {
		seen[requeue.Identity.String()+"\x00"+requeue.From.Format(time.RFC3339Nano)] = true
	}
	for _, requeue := range requeues {
		if seen[requeue.Identity.String()+"\x00"+requeue.From.Format(time.RFC3339Nano)] {
			continue
		}
		if requeue.Identity == chosen.Identity {
			repair.Requeued++
		} else {
			repair.OtherRequeued++
		}
	}
	visible := false
	if err := store.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM retract_visible_memory WHERE id=? AND person=?)", stored.ID, store.person).Scan(&visible)
	}); err != nil {
		return repair, err
	}
	_ = chosenSet
	repair.Clean = !visible && repair.ItemsRetracted == 1 && repair.Requeued == 1 && repair.OtherRequeued == 0
	return repair, nil
}

// OwnerSample is the private run: the owner's exported people records and
// identities, labelled with the record each belongs to or "unknown".
type OwnerSample struct {
	People     []SetPerson       `json:"people"`
	Identities []SetIdentity     `json:"identities"`
	Labels     map[string]string `json:"labels"`
}

// Counts is what the private run writes: counts and rates only, never a name,
// an id or a text.
type Counts struct {
	Identities         int     `json:"identities"`
	UsableLinks        int     `json:"usable_links"`
	WrongLinks         int     `json:"wrong_links"`
	Precision          float64 `json:"precision"`
	LinkedShare        float64 `json:"linked_share"`
	Ambiguities        int     `json:"ambiguities"`
	AmbiguitiesFlagged int     `json:"ambiguities_flagged"`
	Proposals          int     `json:"proposals"`
	ProposalTop1       float64 `json:"proposal_top1"`
	RepairClean        bool    `json:"repair_clean"`
	RuleOfThree        float64 `json:"rule_of_three"`
	Gate               bool    `json:"gate"`
}

// CountsOf projects measures without identifiers.
func CountsOf(m Measures) Counts {
	return Counts{
		Identities: m.Identities, UsableLinks: m.UsableLinks, WrongLinks: m.WrongLinks,
		Precision: m.Precision(), LinkedShare: m.LinkedShare(),
		Ambiguities: m.Ambiguities, AmbiguitiesFlagged: m.AmbiguitiesFlagged,
		Proposals: m.Proposals, ProposalTop1: m.ProposalTop1Rate(),
		RepairClean: m.Repair.Clean, RuleOfThree: m.WrongRateBound(), Gate: m.Gate(),
	}
}

// LoadOwnerSample reads the private run's files from a directory: people.json,
// identities.json and labels.json. The files stay on the owner's machine.
func LoadOwnerSample(dir string) (Set, error) {
	read := func(name string) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	people, err := read("people.json")
	if err != nil {
		return Set{}, err
	}
	identities, err := read("identities.json")
	if err != nil {
		return Set{}, err
	}
	labels, err := read("labels.json")
	if err != nil {
		return Set{}, err
	}
	var sample OwnerSample
	if err := errors.Join(json.Unmarshal(people, &sample.People), json.Unmarshal(identities, &sample.Identities), json.Unmarshal(labels, &sample.Labels)); err != nil {
		return Set{}, err
	}
	for i := range sample.Identities {
		id := sample.Identities[i]
		label, ok := sample.Labels[(Identity{Platform: id.Platform, Scope: id.Scope, User: id.User}).String()]
		if !ok {
			label, ok = sample.Labels[(Identity{Platform: id.Platform, Scope: id.Scope, User: id.User}).Key()]
		}
		if !ok {
			return Set{}, fmt.Errorf("%w: missing label", ErrInvalid)
		}
		if label == "self" {
			sample.Identities[i].Self = true
		}
		sample.Identities[i].Expect = label
	}
	return Set{Version: 1, People: sample.People, Identities: sample.Identities}, nil
}

// WriteCounts writes the private run's counts where the owner asked for them.
func WriteCounts(path string, measures Measures) error {
	data, err := json.MarshalIndent(CountsOf(measures), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Report is the person-facing one-line summary of a synthetic run.
func Report(m Measures) string {
	return fmt.Sprintf("usable links %d, wrong %d, precision %.3f; linked automatically %.3f; ambiguities %d/%d flagged; proposals %d, top-1 %.3f; moved windows %d/%d rejected; repair clean %t; bound 3/n %.4f; gate %t",
		m.UsableLinks, m.WrongLinks, m.Precision(), m.LinkedShare(), m.AmbiguitiesFlagged, m.Ambiguities,
		m.Proposals, m.ProposalTop1Rate(), m.WindowsRejected, m.Windows, m.Repair.Clean, m.WrongRateBound(), m.Gate())
}
