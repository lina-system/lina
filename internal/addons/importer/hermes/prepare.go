// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type profileBuilder struct {
	p                    *Plan
	profile              Profile
	tables               map[SQLiteSource]map[string][]sourceRow
	conversations        map[string]*conversation
	records              map[LegacyKey]*record
	sessions             map[string]Row
	lcm                  map[int64]*record
	sessionConversations map[string]*conversation
}

func newProfile(ctx context.Context, p *Plan, profile Profile) (*profileBuilder, error) {
	b := &profileBuilder{p: p, profile: profile, tables: map[SQLiteSource]map[string][]sourceRow{}, conversations: map[string]*conversation{}, records: map[LegacyKey]*record{}, sessions: map[string]Row{}, lcm: map[int64]*record{}, sessionConversations: map[string]*conversation{}}
	for _, src := range []struct {
		kind SQLiteSource
		path string
	}{{HermesLCM, profile.LCMPath}, {HermesState, profile.StatePath}} {
		if src.path == "" {
			continue
		}
		db, err := openSpeakerSource(ctx, src.path, src.kind)
		if err != nil {
			return nil, err
		}
		tables := map[string][]sourceRow{}
		schema, err := registeredSchema(ctx, db, src.kind)
		if err != nil {
			db.Close()
			return nil, err
		}
		var names []string
		for name := range schema.Tables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cols := schema.Tables[name]
			count := TableCount{System: string(src.kind), Instance: profile.ID, Table: name}
			for _, c := range cols {
				count.Columns = append(count.Columns, c.Name)
			}
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(name)).Scan(&count.Rows); err != nil {
				db.Close()
				return nil, err
			}
			keep := src.kind == HermesLCM && (name == "messages" || name == "host_uid_bindings" || name == "message_relations" || name == "summary_nodes" || name == "summary_node_provenance") || src.kind == HermesState && (name == "messages" || name == "sessions")
			if !keep {
				count.Omitted = "derived index, cache, rollup or operational state outside memory import"
				if src.kind == HermesState && name == "delivery_obligations" {
					count.Omitted = "gateway delivery retry state outside memory import"
				}
			} else {
				rows, err := readRows(ctx, db, name, cols)
				if err != nil {
					db.Close()
					return nil, err
				}
				for _, row := range rows {
					if name == "host_uid_bindings" || name == "message_relations" {
						if count.Kinds == nil {
							count.Kinds = map[string]int{}
						}
						count.Kinds[row.text("kind")]++
					}
					k := rowKey(name, row, cols)
					if k == "" {
						db.Close()
						return nil, invalidSnapshot("source row key required")
					}
					tables[name] = append(tables[name], sourceRow{key: LegacyKey{string(src.kind), profile.ID, name, k}, row: row})
				}
				sort.Slice(tables[name], func(i, j int) bool {
					a, b := tables[name][i].key.Key, tables[name][j].key.Key // numeric source order, not lexicographic
					if name == "messages" {
						field := "id"
						if src.kind == HermesLCM {
							field = "store_id"
						}
						return tables[name][i].row.integer(field) < tables[name][j].row.integer(field)
					}
					return a < b
				})
			}
			p.inventory = append(p.inventory, count)
		}
		for name, cols := range schema.Views {
			count := TableCount{System: string(src.kind), Instance: profile.ID, Table: name, Omitted: "derived source view"}
			for _, c := range cols {
				count.Columns = append(count.Columns, c.Name)
			}
			if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(name)).Scan(&count.Rows); err != nil {
				db.Close()
				return nil, err
			}
			p.inventory = append(p.inventory, count)
		}
		db.Close()
		b.tables[src.kind] = tables
	}
	for _, s := range b.tables[HermesState]["sessions"] {
		b.sessions[s.row.text("id")] = s.row
	}
	for _, s := range b.tables[HermesLCM]["messages"] {
		row := s.row
		session := row.text("session_id")
		conv := row.text("conversation_id")
		if conv == "" {
			conv = "session:" + session
		}
		c := b.conversation("lcm:" + conv)
		if old := b.sessionConversations[session]; old != nil && old != c {
			return nil, fmt.Errorf("%w: session spans conversations", ErrSnapshot)
		}
		b.sessionConversations[session] = c
		r := b.message(c, s, 4)
		r.event.lcmIDs = []int64{row.integer("store_id")}
		b.lcm[row.integer("store_id")] = r
	}
	bindings := map[string][]sourceRow{}
	for _, s := range b.tables[HermesLCM]["host_uid_bindings"] {
		kind := s.row.text("kind")
		id := s.row.integer("store_id")
		if s.row.integer("binding_version") != 1 {
			return nil, invalidSnapshot("host_uid_bindings.binding_version must be 1")
		}
		if kind == "engine" && id == 0 {
			c := b.conversation("lineage:" + s.row.text("lineage_key"))
			r := b.add(c, "uid_binding", 4, s)
			r.flags = []string{"engine"}
			continue
		}
		if kind != "canonical" && kind != "version" && kind != "alias_candidate" {
			return nil, fmt.Errorf("%w: binding kind", ErrSnapshot)
		}
		r := b.lcm[id]
		if r == nil {
			return nil, ErrSourceMissing
		}
		b.attach(r, s)
		// A shadow alias is evidence about a candidate, not a confirmed identity.
		if kind == "alias_candidate" {
			continue
		}
		k := s.row.text("lineage_key") + "\x00" + s.row.text("uid")
		bindings[k] = append(bindings[k], s)
	}
	for _, ss := range bindings {
		var original *record
		for _, s := range ss {
			if s.row.text("kind") == "canonical" {
				if original != nil {
					return nil, invalidSnapshot("unique canonical UID binding")
				}
				original = b.lcm[s.row.integer("store_id")]
			}
		}
		if original == nil {
			continue
		}
		for _, s := range ss {
			if s.row.text("kind") != "version" {
				continue
			}
			r := b.lcm[s.row.integer("store_id")]
			if r.id == original.id {
				return nil, invalidSnapshot("version must differ from canonical event")
			}
			r.links = append(r.links, importLink{original.id, "supersedes", 0})
			if samePayload(r.sources[0].row, original.sources[0].row) {
				r.event.replay = original.id
				r.flags = append(r.flags, "replay")
			}
		}
	}
	// Index within this profile, so each host row considers only its UID's
	// lineages instead of scanning every binding in the snapshot.
	byUID := map[string]map[string][]sourceRow{}
	for _, ss := range bindings {
		uid, lineage := ss[0].row.text("uid"), ss[0].row.text("lineage_key")
		if byUID[uid] == nil {
			byUID[uid] = map[string][]sourceRow{}
		}
		byUID[uid][lineage] = ss
	}
	hostUID := map[string]*record{}
	for _, s := range b.tables[HermesState]["messages"] {
		row := s.row
		session := row.text("session_id")
		if b.sessions[session] == nil {
			return nil, ErrSourceMissing
		}
		root, err := b.lineage(session)
		if err != nil {
			return nil, err
		}
		uid := row.text("message_uid")
		var target *record
		var mapping *UIDLineageMapping
		if uid != "" {
			var candidates []sourceRow
			candidates, mapping = selectUIDLineage(byUID[uid], root)
			for _, binding := range candidates {
				r := b.lcm[binding.row.integer("store_id")]
				if binding.row.text("kind") == "canonical" {
					if target != nil {
						return nil, invalidSnapshot("unique host canonical UID target")
					}
					target = r
				}
			}
			// An identical version is a replay, not an ambiguous host identity.
			// Prefer canonical when its bytes match; otherwise require a unique
			// payload version and preserve every other source event separately.
			if target == nil || !samePayload(row, target.sources[0].row) {
				var version *record
				for _, binding := range candidates {
					r := b.lcm[binding.row.integer("store_id")]
					if binding.row.text("kind") == "version" && samePayload(row, r.sources[0].row) {
						if version != nil && version != r {
							return nil, invalidSnapshot("unique matching UID payload version")
						}
						version = r
					}
				}
				if version != nil {
					target = version
				}
			}
			if target == nil && mapping == nil {
				target = hostUID[root+"\x00"+uid]
			}
		}
		if target == nil {
			if mapping != nil && mapping.Lineage != "" {
				mapping.Lineage = ""
				mapping.Reason = "no_matching_message"
			}
			c := b.sessionConversations[session]
			if c == nil {
				c = b.conversation("host:" + root)
				b.sessionConversations[session] = c
			}
			target = b.message(c, s, 4)
		} else {
			b.attach(target, s)
		}
		if mapping != nil {
			mapping.Source, mapping.EventID = s.key, target.id
			p.uidLineages = append(p.uidLineages, *mapping)
		}
		if b.sessionConversations[session] == nil {
			b.sessionConversations[session] = b.byID(target.conversation)
		}
		target.event.hostIDs = append(target.event.hostIDs, row.integer("id"))
		if len(target.event.hostIDs) == 1 {
			target.event.occurred = timestamp(row, "timestamp")
			target.event.quality = "observed"
		}
		if uid != "" {
			hostUID[root+"\x00"+uid] = target
		}
	}
	for _, s := range b.tables[HermesState]["sessions"] {
		session := s.row.text("id")
		c := b.sessionConversations[session]
		if c == nil {
			root, err := b.lineage(session)
			if err != nil {
				return nil, err
			}
			c = b.conversation("host:" + root)
			b.sessionConversations[session] = c
		}
		b.attach(c.record, s)
		if s.row.text("source") == "cron" {
			c.hidden = true
		}
	}
	for _, s := range b.tables[HermesLCM]["message_relations"] {
		from, to := b.lcm[s.row.integer("store_id")], b.lcm[s.row.integer("related_store_id")]
		if from == nil || to == nil {
			return nil, ErrSourceMissing
		}
		kind := s.row.text("kind")
		if kind != "view_of" && kind != "supersedes" && kind != "pairs_with" && kind != "composite" {
			return nil, invalidSnapshot("message_relations.kind registered")
		}
		r := b.add(b.byID(from.conversation), "relation", 4, s)
		r.links = []importLink{{from.id, "from", 0}, {to.id, kind, int(s.row.integer("ordinal"))}}
		if kind == "composite" {
			// A head is itself a constituent, not an event containing the entire
			// composite. Retain group provenance and member order only as source links.
			r.kind = "composite"
		}
	}
	for _, s := range b.tables[HermesLCM]["summary_nodes"] {
		c := b.sessionConversations[s.row.text("session_id")]
		if c == nil {
			return nil, ErrSourceMissing
		}
		r := b.add(c, "summary", 7, s)
		r.body = s.row.text("summary")
		r.sources[0].bodyColumn = "summary"
		r.flags = []string{"legacy", "unverified"}
	}
	for _, s := range b.tables[HermesLCM]["summary_nodes"] {
		r := b.records[s.key]
		var ids []int64
		if err := json.Unmarshal([]byte(s.row.text("source_ids")), &ids); err != nil {
			return nil, invalidSnapshot("summary_nodes.source_ids JSON")
		}
		kind := s.row.text("source_type")
		if kind != "messages" && kind != "nodes" {
			return nil, invalidSnapshot("summary_nodes.source_type registered")
		}
		for i, id := range ids {
			var child *record
			if kind == "messages" {
				child = b.lcm[id]
			} else {
				child = b.records[LegacyKey{string(HermesLCM), profile.ID, "summary_nodes", fmt.Sprint(id)}]
			}
			if child == nil {
				return nil, ErrSourceMissing
			}
			if child.conversation != r.conversation || child.id == r.id {
				return nil, invalidSnapshot("summary child must be distinct and in same conversation")
			}
			r.links = append(r.links, importLink{child.id, "child", i})
		}
	}
	for _, s := range b.tables[HermesLCM]["summary_node_provenance"] {
		r := b.records[LegacyKey{string(HermesLCM), profile.ID, "summary_nodes", s.key.Key}]
		if r == nil {
			return nil, ErrSourceMissing
		}
		b.attach(r, s)
		if s.row.integer("escalation_level") > 0 {
			r.flags = append(r.flags, "degraded")
		}
	}
	return b, nil
}

// The original home path is not available in a staged snapshot. Match the
// compression root only when it identifies one lineage; never guess between
// two home tags or follow a binding across a fork/reset boundary.
func selectUIDLineage(lineages map[string][]sourceRow, root string) ([]sourceRow, *UIDLineageMapping) {
	var names, matching []string
	for lineage := range lineages {
		names = append(names, lineage)
		if lineage == root || strings.HasSuffix(lineage, ":"+root) {
			matching = append(matching, lineage)
		}
	}
	var candidates []sourceRow
	if len(matching) == 1 {
		candidates = lineages[matching[0]]
	}
	if len(names) <= 1 {
		return candidates, nil
	}
	sort.Strings(names)
	mapping := &UIDLineageMapping{SessionRoot: root, CandidateLineages: names}
	switch len(matching) {
	case 0:
		mapping.Reason = "no_root_lineage"
	case 1:
		mapping.Lineage, mapping.Reason = matching[0], "session_root"
	default:
		mapping.Reason = "multiple_root_lineages"
	}
	return candidates, mapping
}
func samePayload(a, b Row) bool {
	for _, f := range []string{"role", "content", "tool_calls", "tool_call_id", "tool_name"} {
		if a[f] != b[f] {
			return false
		}
	}
	return true
}
func (b *profileBuilder) lineage(session string) (string, error) {
	seen := map[string]bool{}
	for {
		if seen[session] {
			return "", invalidSnapshot("session lineage cycle")
		}
		seen[session] = true
		r := b.sessions[session]
		if r == nil {
			return "", ErrSourceMissing
		}
		parent := r.text("parent_session_id")
		if parent == "" || b.sessions[parent] == nil || b.sessions[parent].text("end_reason") != "compression" || explicitFork(r) {
			return session, nil
		}
		session = parent
	}
}

// Matches LCM-X host_uid.py:41-55: only a marker naming this parent is a fork.
func explicitFork(r Row) bool {
	if r.text("source") == "tool" {
		return true
	}
	var config map[string]any
	if json.Unmarshal([]byte(r.text("model_config")), &config) != nil {
		return false
	}
	parent := r.text("parent_session_id")
	for _, key := range []string{"_branched_from", "_delegate_from", "_reset_from"} {
		marker, ok := config[key]
		if !ok || marker == nil {
			continue
		}
		if parent == "" || marker == parent {
			return true
		}
	}
	return false
}

func (b *profileBuilder) conversation(source string) *conversation {
	if c := b.conversations[source]; c != nil {
		return c
	}
	id := opaque(b.profile.ID, "conversation", source)
	r := &record{id: id, conversation: id, agent: b.profile.AgentID, kind: "conversation", stage: 3, flags: []string{"imported"}}
	c := &conversation{record: r, records: []*record{r}}
	b.conversations[source] = c
	b.p.conversations = append(b.p.conversations, c)
	return c
}
func (b *profileBuilder) byID(id string) *conversation {
	for _, c := range b.conversations {
		if c.record.id == id {
			return c
		}
	}
	return nil
}
func (b *profileBuilder) add(c *conversation, kind string, stage int, s sourceRow) *record {
	r := &record{id: keyID(s.key), conversation: c.record.id, agent: b.profile.AgentID, kind: kind, stage: stage}
	c.records = append(c.records, r)
	b.attach(r, s)
	return r
}
func (b *profileBuilder) attach(r *record, s sourceRow) {
	if s.key.Kind == "messages" && r.event != nil && s.row["content"].Kind == "text" && s.row.text("content") == r.body {
		s.bodyColumn = "content"
	}
	r.sources = append(r.sources, s)
	b.records[s.key] = r
}
func (b *profileBuilder) message(c *conversation, s sourceRow, stage int) *record {
	r := b.add(c, "event", stage, s)
	r.body = s.row.text("content")
	r.event = &event{session: s.row.text("session_id"), role: s.row.text("role"), recorded: timestamp(s.row, "timestamp"), occurred: timestamp(s.row, "timestamp"), quality: "write_time_only"}
	if s.row["content"].Kind == "text" {
		r.sources[0].bodyColumn = "content"
	}
	if s.row["observed_at"].Kind != "null" && s.row["observed_at"].Kind != "" {
		r.event.occurred = timestamp(s.row, "observed_at")
		r.event.quality = "observed"
	}
	return r
}
func (b *profileBuilder) finish(ctx context.Context) error {
	in := SpeakerProfile{Profile: b.profile.ID, PersonID: b.profile.PersonID, StatePath: b.profile.StatePath, LCMPath: b.profile.LCMPath, OwnerChannels: b.profile.OwnerChannels}
	var records []*record
	for _, c := range b.conversations {
		for _, r := range c.records {
			if r.event != nil {
				in.Events = append(in.Events, SpeakerSources{ID: r.id, HostIDs: r.event.hostIDs, LCMIDs: r.event.lcmIDs, HonchoOwner: r.event.honchoOwner})
				records = append(records, r)
			}
		}
	}
	report, err := ClassifySpeakers(ctx, in)
	if err != nil {
		return err
	}
	for i, e := range report.Events {
		records[i].event.classified = e
	}
	// UID aliases may repeat an edge. Stabilize and deduplicate derived
	// metadata so a new preparation has the same manifest independent of maps.
	for _, c := range b.conversations {
		for _, r := range c.records {
			sort.Strings(r.flags)
			r.flags = compactStrings(r.flags)
			sort.Slice(r.links, func(i, j int) bool {
				a, z := r.links[i], r.links[j]
				if a.kind != z.kind {
					return a.kind < z.kind
				}
				if a.ordinal != z.ordinal {
					return a.ordinal < z.ordinal
				}
				return a.target < z.target
			})
			links := r.links[:0]
			for _, link := range r.links {
				if len(links) == 0 || links[len(links)-1] != link {
					links = append(links, link)
				}
			}
			r.links = links
		}
	}
	// All summary links must form a DAG, including forward links.
	all := map[string]*record{}
	for _, c := range b.conversations {
		for _, r := range c.records {
			all[r.id] = r
		}
	}
	active, done := map[string]bool{}, map[string]bool{}
	var visit func(*record) error
	visit = func(r *record) error {
		if active[r.id] {
			return invalidSnapshot("summary child cycle")
		}
		if done[r.id] {
			return nil
		}
		active[r.id] = true
		for _, l := range r.links {
			if l.kind == "child" {
				child := all[l.target]
				if child == nil {
					return ErrSourceMissing
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		active[r.id] = false
		done[r.id] = true
		return nil
	}
	for _, r := range all {
		if err := visit(r); err != nil {
			return err
		}
	}
	for _, c := range b.conversations {
		sort.SliceStable(c.records, func(i, j int) bool { return c.records[i].stage < c.records[j].stage })
		c.report = SpeakerReport{Profile: b.profile.ID}
		parent := ""
		for _, r := range c.records {
			if r.event == nil {
				continue
			}
			r.event.parent = parent
			parent = r.id
			e := r.event.classified
			c.report.Events = append(c.report.Events, e)
			if e.relabelled {
				c.report.Relabelled++
			}
			if e.unproven {
				c.report.Unproven++
			}
		}
	}
	return nil
}

func compactStrings(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
