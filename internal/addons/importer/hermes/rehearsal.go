// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type ProfileMapping struct {
	Profile, Agent, Person string
	OwnerChannels          []OwnerChannel
}

// SourceGap names absent numeric ids between observed source rows. It does not
// invent missing content or imply that source ids should have been contiguous.
type SourceGap struct {
	System, Instance, Table string
	After, Before           int64
}

// RehearsalReport contains counts and opaque identities, never source bodies
// or credentials. Omitted columns are enumerated in Tables with their reason.
type RehearsalReport struct {
	Mappings                                     []ProfileMapping
	Conversations, Events, Records, References   int
	Tables                                       []Reconciliation
	Inventory                                    []TableCount
	Speakers                                     []SpeakerReport
	ReplayEvents, HonchoMatched, HonchoUnmatched int
	SecretSpans                                  int
	MissingBytes                                 []string
	Gaps                                         []SourceGap
	SampledReferences                            []LegacyKey
}

// Rehearse uses only the prepared snapshot. It neither opens nor writes canon.
// All keys are checked; the deterministic sample is an additional report aid.
func (p *Plan) Rehearse() (RehearsalReport, error) {
	p.prepareRehearsal()
	// Reports belong to callers; copying preserves immutable speaker decisions
	// too, whose private fields deliberately cannot be decoded from JSON.
	report := p.rehearsalReport
	report.Mappings = slices.Clone(report.Mappings)
	for i := range report.Mappings {
		report.Mappings[i].OwnerChannels = slices.Clone(report.Mappings[i].OwnerChannels)
	}
	report.Tables = slices.Clone(report.Tables)
	report.Inventory = slices.Clone(report.Inventory)
	for i := range report.Inventory {
		report.Inventory[i].Columns = slices.Clone(report.Inventory[i].Columns)
		report.Inventory[i].Kinds = maps.Clone(report.Inventory[i].Kinds)
	}
	report.Speakers = slices.Clone(report.Speakers)
	for i := range report.Speakers {
		report.Speakers[i].Events = slices.Clone(report.Speakers[i].Events)
	}
	report.MissingBytes = slices.Clone(report.MissingBytes)
	report.Gaps = slices.Clone(report.Gaps)
	report.SampledReferences = slices.Clone(report.SampledReferences)
	return report, p.rehearsalErr
}

func (p *Plan) prepareRehearsal() {
	p.rehearsalOnce.Do(func() {
		p.rehearsalReport, p.rehearsalErr = p.rehearse()
		if p.rehearsalErr != nil {
			return
		}
		var encoded []byte
		encoded, p.rehearsalErr = json.Marshal(p.rehearsalReport)
		p.rehearsalJSON = string(encoded)
	})
}

func (p *Plan) rehearseSnapshot() (RehearsalReport, error) {
	report := RehearsalReport{Conversations: len(p.conversations), Inventory: p.inventory, Mappings: p.mappings}
	counts := map[string]*Reconciliation{}
	destinations := map[string]map[string]bool{}
	for _, t := range p.inventory {
		id := opaque(t.System, t.Instance, t.Table)
		counts[id] = &Reconciliation{System: t.System, Instance: t.Instance, Table: t.Table, SnapshotRows: t.Rows, Omitted: t.Omitted}
		destinations[id] = map[string]bool{}
	}
	speakers := map[string]*SpeakerReport{}
	for _, profile := range p.profiles {
		speakers[profile] = &SpeakerReport{Profile: profile}
	}
	seen := map[LegacyKey]bool{}
	var sample []LegacyKey
	for _, c := range p.conversations {
		s := speakers[c.report.Profile]
		s.Relabelled += c.report.Relabelled
		s.Unproven += c.report.Unproven
		s.Events = append(s.Events, c.report.Events...)
		for _, r := range c.records {
			report.Records++
			if r.event != nil {
				report.Events++
				if r.event.replay != "" {
					report.ReplayEvents++
				}
			}
			for _, flag := range r.flags {
				if flag == "bytes_unavailable" {
					report.MissingBytes = append(report.MissingBytes, r.id)
				}
			}
			for _, src := range r.sources {
				if seen[src.key] {
					return report, ErrConflict
				}
				seen[src.key] = true
				report.References++
				id := opaque(src.key.System, src.key.Instance, src.key.Kind)
				count := counts[id]
				if count == nil {
					return report, fmt.Errorf("%w: unreported source kind", ErrConflict)
				}
				count.MappedRows++
				count.References++
				destinations[id][r.id] = true
				for column, v := range src.row {
					if v.Kind == "text" || v.Kind == "blob" {
						report.SecretSpans += len(ScanSecrets([]byte(src.row.text(column))))
					}
				}
				if src.key.System == "honcho" && src.key.Kind == "messages" {
					if r.stage == 6 {
						report.HonchoUnmatched++
					} else {
						report.HonchoMatched++
					}
				}
				sample = append(sample, src.key)
			}
		}
	}
	for _, profile := range p.profiles {
		report.Speakers = append(report.Speakers, *speakers[profile])
	}
	for _, t := range p.inventory {
		id := opaque(t.System, t.Instance, t.Table)
		c := counts[id]
		c.DistinctRecords = len(destinations[id])
		expected := t.Rows
		if t.Omitted != "" {
			expected = 0
		}
		if c.MappedRows != expected {
			return report, ErrConflict
		}
		report.Tables = append(report.Tables, *c)
	}
	sort.Slice(sample, func(i, j int) bool { return digest(sample[i]) < digest(sample[j]) })
	sampled := map[string]int{}
	for _, key := range sample {
		if sampled[key.System] < 100 {
			report.SampledReferences = append(report.SampledReferences, key)
			sampled[key.System]++
		}
	}

	numeric := map[string][]int64{}
	namespaces := map[string]LegacyKey{}
	for _, key := range sample {
		if key.Kind != "messages" {
			continue
		}
		n, err := strconv.ParseInt(key.Key, 10, 64)
		if err != nil {
			continue
		}
		ns := opaque(key.System, key.Instance, key.Kind)
		numeric[ns] = append(numeric[ns], n)
		namespaces[ns] = key
	}
	for ns, ids := range numeric {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		key := namespaces[ns]
		for i := 1; i < len(ids); i++ {
			if ids[i] > ids[i-1] && ids[i]-1 > ids[i-1] {
				report.Gaps = append(report.Gaps, SourceGap{key.System, key.Instance, key.Kind, ids[i-1], ids[i]})
			}
		}
	}
	sort.Slice(report.Gaps, func(i, j int) bool { return fmt.Sprint(report.Gaps[i]) < fmt.Sprint(report.Gaps[j]) })

	return report, nil
}

func hasFlag(r *record, flag string) bool {
	for _, f := range r.flags {
		if f == flag || strings.HasPrefix(f, flag+":") {
			return true
		}
	}
	return false
}
