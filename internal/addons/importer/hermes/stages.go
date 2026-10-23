// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrSourceMissing = errors.New("import_source_missing")
var ErrConflict = errors.New("import_source_conflict")

// Profile is one confirmed destination and its immutable staged snapshots.
// Empty paths mean an absent source, never a live source to create or repair.
type Profile struct {
	ID, AgentID, PersonID, StatePath, LCMPath string
	OwnerChannels                             []OwnerChannel
}
type Request struct {
	// Materials is Core-owned; required for present sidecars. Prepare stages bytes
	// and Plan.Close reconciles them after all enclosing transactions finish.
	Materials *assets.MaterialWriter
	BatchID   string
	Confirmed bool
	Rehearsal bool
	Profiles  []Profile
	Sidecars  []Sidecar
}
type LegacyKey struct{ System, Instance, Kind, Key string }
type sourceRow struct {
	key        LegacyKey
	row        Row
	bodyColumn string
}
type importLink struct {
	target, kind string
	ordinal      int
}
type record struct {
	material                            *assets.PreparedMaterial
	id, conversation, agent, kind, body string
	stage                               int
	sources                             []sourceRow
	flags                               []string
	links                               []importLink
	event                               *event
}
type event struct {
	session, role, occurred, recorded, quality, parent, replay string
	hostIDs, lcmIDs                                            []int64
	honchoOwner                                                bool
	classified                                                 ClassifiedEvent
}
type conversation struct {
	record  *record
	records []*record
	report  SpeakerReport
	hidden  bool
}
type TableCount struct {
	Kinds                   map[string]int `json:",omitempty"`
	System, Instance, Table string
	Columns                 []string
	Rows                    int
	Omitted                 string
}
type Plan struct {
	batch         string
	rehearsal     bool
	people        map[string]string
	mappings      []ProfileMapping
	profiles      []string
	conversations []*conversation
	inventory     []TableCount
	uidLineages   []UIDLineageMapping
}
type Report struct {
	AddedEvents, AddedRecords, AddedReferences int
	Tables                                     []TableCount
	Speakers                                   []SpeakerReport
	AmbiguousUIDRows                           int
	UIDLineageMappings                         []UIDLineageMapping
}
type Reconciliation struct {
	System, Instance, Table                               string
	SnapshotRows, MappedRows, DistinctRecords, References int
	Omitted                                               string
}
type Verification struct {
	Rows   int
	Tables []Reconciliation
}
type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

func opaque(parts ...string) string { return "imp_" + digest(parts) }
func keyID(k LegacyKey) string      { return opaque(k.System, k.Instance, k.Kind, k.Key) }
func (p *Plan) Keys() []LegacyKey {
	var keys []LegacyKey
	for _, c := range p.conversations {
		for _, r := range c.records {
			for _, s := range r.sources {
				keys = append(keys, s.key)
			}
		}
	}
	return keys
}
func (p *Plan) Conversations() int { return len(p.conversations) }

// Prepare performs all schema, identity, link and attribution checks before any
// canon write. The returned plan owns its source data and does not retain a DB.
func Prepare(ctx context.Context, req Request) (*Plan, error) { return prepare(ctx, req, nil) }
func prepare(ctx context.Context, req Request, honcho []honchoWorkspace) (_ *Plan, err error) {
	if !req.Confirmed || req.BatchID == "" || len(req.Profiles) == 0 {
		return nil, invalidSnapshot("confirmed maps, batch and profiles required")
	}
	p := &Plan{batch: req.BatchID, rehearsal: req.Rehearsal, people: map[string]string{}}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	for _, profile := range req.Profiles {
		p.profiles = append(p.profiles, profile.ID)
		p.people[profile.ID] = profile.PersonID
		candidates, err := DiscoverOwnerChannels(ctx, profile.StatePath)
		if err != nil {
			return nil, err
		}
		p.mappings = append(p.mappings, ProfileMapping{Profile: profile.ID, Agent: profile.AgentID, Person: profile.PersonID, OwnerChannels: profile.OwnerChannels, CandidateOwnerChannels: candidates})
	}
	seen := map[string]bool{}
	builders := map[string]*profileBuilder{}
	for _, profile := range req.Profiles {
		if profile.ID == "" || profile.AgentID == "" || profile.PersonID == "" || seen[profile.ID] {
			return nil, invalidSnapshot("unique complete profile mapping")
		}
		seen[profile.ID] = true
		b, err := newProfile(ctx, p, profile)
		if err != nil {
			return nil, err
		}
		builders[profile.ID] = b
	}
	for _, w := range honcho {
		b := builders[w.profile]
		if b == nil {
			return nil, invalidSnapshot("Honcho profile mapping required")
		}
		if err := b.addHoncho(w); err != nil {
			return nil, err
		}
	}
	for _, s := range req.Sidecars {
		b := builders[s.Event.Instance]
		if b == nil {
			return nil, invalidSnapshot("sidecar profile mapping required")
		}
		if err := b.addSidecar(ctx, s, req.Materials); err != nil {
			return nil, err
		}
	}
	for _, profile := range req.Profiles {
		b := builders[profile.ID]
		if err := b.finish(ctx); err != nil {
			return nil, err
		}
	}
	sort.Slice(p.conversations, func(i, j int) bool { return p.conversations[i].record.id < p.conversations[j].record.id })
	if err := p.orderConversations(); err != nil {
		return nil, err
	}
	keys := map[LegacyKey]bool{}
	for _, k := range p.Keys() {
		if keys[k] {
			return nil, fmt.Errorf("%w: repeated source key", ErrSnapshot)
		}
		keys[k] = true
	}
	return p, nil
}
func timestamp(r Row, field string) string {
	n := r.number(field)
	seconds := int64(n)
	return time.Unix(seconds, int64((n-float64(seconds))*1e9)).UTC().Format(time.RFC3339Nano)
}
func (p *Plan) Import(ctx context.Context, db Canon) (Report, error) {
	if p.rehearsal {
		return Report{}, invalidSnapshot("rehearsal plan cannot import")
	}
	defer p.Close() // durable markers preserve cleanup failures for restart recovery
	report := Report{Tables: append([]TableCount(nil), p.inventory...), AmbiguousUIDRows: len(p.uidLineages), UIDLineageMappings: p.uidLineages}
	byProfile := map[string]int{}
	for _, profile := range p.profiles {
		byProfile[profile] = len(report.Speakers)
		report.Speakers = append(report.Speakers, SpeakerReport{Profile: profile})
	}
	for i, c := range p.conversations {
		var added Report
		err := db.Write(ctx, func(tx *canon.Tx) error { var err error; added, err = p.Apply(ctx, tx, i); return err })
		if err != nil {
			return report, err
		}
		report.AddedEvents += added.AddedEvents
		report.AddedRecords += added.AddedRecords
		report.AddedReferences += added.AddedReferences
		speaker := &report.Speakers[byProfile[c.report.Profile]]
		speaker.Relabelled += c.report.Relabelled
		speaker.Unproven += c.report.Unproven
		speaker.Events = append(speaker.Events, c.report.Events...)
	}
	return report, nil
}

// Resolve cross-conversation references before their dependent conversation.
// A cycle cannot be committed in separate transactions and is refused during
// preparation, before any range is published.
func (p *Plan) orderConversations() error {
	owners := map[string]*conversation{}
	for _, c := range p.conversations {
		for _, r := range c.records {
			owners[r.id] = c
		}
	}
	active, done := map[string]bool{}, map[string]bool{}
	var ordered []*conversation
	var visit func(*conversation) error
	visit = func(c *conversation) error {
		id := c.record.id
		if active[id] {
			return fmt.Errorf("%w: conversation dependency cycle", ErrSnapshot)
		}
		if done[id] {
			return nil
		}
		active[id] = true
		for _, r := range c.records {
			for _, link := range r.links {
				target := owners[link.target]
				if target == nil {
					return ErrSourceMissing
				}
				if target != c {
					if err := visit(target); err != nil {
						return err
					}
				}
			}
		}
		active[id] = false
		done[id] = true
		ordered = append(ordered, c)
		return nil
	}
	for _, c := range p.conversations {
		if err := visit(c); err != nil {
			return err
		}
	}
	p.conversations = ordered
	sort.Slice(p.inventory, func(i, j int) bool {
		a, b := p.inventory[i], p.inventory[j]
		if a.System != b.System {
			return a.System < b.System
		}
		if a.Instance != b.Instance {
			return a.Instance < b.Instance
		}
		return a.Table < b.Table
	})
	return nil
}

// Close must run after any enclosing Core transaction, including failed job
// adoption. Prepared material bytes have durable recovery markers until then.
// A plan with sidecars is one adoption attempt; prepare again after a rollback.
func (p *Plan) Close() error {
	var failures []error
	for _, c := range p.conversations {
		for _, r := range c.records {
			if r.material != nil {
				failures = append(failures, r.material.Close())
			}
		}
	}
	return errors.Join(failures...)
}
