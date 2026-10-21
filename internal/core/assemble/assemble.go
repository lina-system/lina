// SPDX-License-Identifier: Apache-2.0

// Package assemble builds model input from caller-supplied, authorized snapshots.
// It reads no clock, store, client history, or provider. Counter must be pure.
package assemble

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/kit/provider"
)

type Kind uint8

const (
	Clock Kind = iota + 1
	Commitments
	Waiting
	SkillBody
	Profile
	Memory
	PersonBrief
	Material
	External
)

type Ref struct {
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
}
type Skill struct{ ID, Description string }
type ContextItem struct {
	ID, Revision, AgentID string // Empty AgentID denotes common knowledge.
	Kind                  Kind
	Text                  string
	Origin                string // A nonempty source locator always denotes evidence, regardless of Kind.
	Evidence              []Ref
	Supersedes            []string
	Rank                  int // Higher ranks survive longer within an optional group.
	Retracted, Secret     bool
}
type ContextPacket []ContextItem
type Input struct {
	Core, AgentID, PersonaRevision string
	Persona                        agents.Layers
	Catalog                        []Skill
	AlwaysTools, LoadedTools       []provider.ToolDecl // Already in stable declaration order.
	Summary                        provider.Item
	History                        []provider.Item // Native ledger projection, in ledger order; never imported history.
	Packet                         ContextPacket
	Current                        provider.Item
	Model                          provider.ModelRef
	Effort                         provider.Effort
	Fields                         provider.FieldList
	MaxTokens                      int // Total request budget, including mandatory context and tools.
}
type Counter func(provider.Request) int
type Reason string

const (
	Invalid    Reason = "invalid_input"
	TooLarge   Reason = "request_too_large"
	OutOfScope Reason = "out_of_scope"
	Excluded   Reason = "excluded"
	Duplicate  Reason = "duplicate"
	Superseded Reason = "superseded"
	Budget     Reason = "budget"
)

type Drop struct {
	ID     string
	Reason Reason
}

// Report is safe to record: it contains identities, digests and counts, no bodies.
type Report struct {
	AgentID, PersonaRevision, Hash string
	Skills, Items                  []Ref
	Dropped                        []Drop
	Tokens                         int
	Failure                        Reason
}

func message(role, text string) provider.Item {
	b, _ := json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{role, text})
	return b
}
func clone(items []provider.Item) []provider.Item {
	out := make([]provider.Item, len(items))
	for i, item := range items {
		out[i] = append(provider.Item(nil), item...)
	}
	return out
}

// Digest covers the logical model request, including exact opaque item bytes.
// Credentials and a runtime's live tool bridge are deliberately not input data.
func Digest(r provider.Request) string {
	h := sha256.New()
	write := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	write([]byte("lina-request-v1"))
	meta, _ := json.Marshal([]string{string(r.Model), string(r.Effort), r.Instructions, string(r.Purpose)})
	write(meta)
	fields, _ := json.Marshal(r.Fields)
	write(fields)
	for _, list := range [][]provider.Item{r.Tools, r.Items} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(list)))
		h.Write(n[:])
		for _, raw := range list {
			write(raw)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func group(k Kind) int {
	switch k {
	case Clock:
		return 1
	case Commitments:
		return 2
	case Waiting:
		return 3
	case SkillBody:
		return 4
	case Profile, Memory, PersonBrief:
		return 5
	case Material:
		return 6
	case External:
		return 7
	}
	return 0
}
func dropPriority(k Kind) int {
	switch k {
	case Clock, Commitments, Waiting, SkillBody:
		return 0
	case Profile, PersonBrief:
		return 1
	case Memory:
		return 2
	case Material:
		return 3
	case External:
		return 4
	}
	return 0
}

func contextMessage(item ContextItem) (provider.Item, bool) {
	text := item.Text
	refs := append([]Ref{{ID: item.ID, Revision: item.Revision}}, item.Evidence...)
	raw, _ := json.Marshal(refs)
	text += "\nReferences: " + string(raw)
	origin := item.Origin
	evidence := item.Kind == External || origin != ""
	switch item.Kind {
	case Profile, Memory, PersonBrief, Material:
		// Retrieved data does not acquire instruction authority when a source
		// locator is missing. Its stable reference still identifies the source.
		evidence = true
		if origin == "" {
			origin = "context:" + item.ID
		}
	case Clock, Commitments, Waiting, SkillBody, External:
	}
	if evidence {
		m, err := guard.Admit(guard.EntryTool, origin, text)
		if err != nil {
			return nil, false
		}
		e, _ := m.Evidence()
		return message(e.Role, e.Text), true
	}
	return message("developer", text), true
}

func Assemble(in Input, counter Counter) (provider.Request, Report) {
	report := Report{AgentID: in.AgentID, PersonaRevision: in.PersonaRevision}
	fail := func(reason Reason) (provider.Request, Report) {
		report.Failure = reason
		return provider.Request{}, report
	}
	if in.AgentID == "" || in.PersonaRevision == "" || in.Model == "" || in.MaxTokens <= 0 || counter == nil || !json.Valid(in.Current) {
		return fail(Invalid)
	}
	if len(in.Summary) > 0 && !json.Valid(in.Summary) {
		return fail(Invalid)
	}
	for _, list := range [][]provider.Item{in.History, in.AlwaysTools, in.LoadedTools} {
		for _, raw := range list {
			if !json.Valid(raw) {
				return fail(Invalid)
			}
		}
	}
	r := provider.Request{Model: in.Model, Effort: in.Effort, Purpose: provider.Turn, Fields: append(provider.FieldList(nil), in.Fields...)}
	for _, text := range []string{in.Core, in.Persona.Core, in.Persona.DefaultVoice, in.Persona.UserVoice, in.Persona.GrowthSnapshot} {
		r.Items = append(r.Items, message("developer", text))
	}
	catalog, _ := json.Marshal(in.Catalog)
	r.Items = append(r.Items, message("developer", string(catalog)))
	r.Tools = append(clone(in.AlwaysTools), clone(in.LoadedTools)...)
	if len(in.Summary) > 0 {
		r.Items = append(r.Items, append(provider.Item(nil), in.Summary...))
	}
	r.Items = append(r.Items, clone(in.History)...)
	prefix := len(r.Items)
	var packet ContextPacket
	seen := map[string]Kind{}
	for _, item := range in.Packet {
		reason := Reason("")
		switch {
		case item.AgentID != "" && item.AgentID != in.AgentID:
			reason = OutOfScope
		case item.Retracted || item.Secret:
			reason = Excluded
		case seen[item.ID] != 0:
			if seen[item.ID] != item.Kind {
				return fail(Invalid)
			}
			reason = Duplicate
		}
		if reason != "" {
			report.Dropped = append(report.Dropped, Drop{item.ID, reason})
			continue
		}
		if item.ID == "" || group(item.Kind) == 0 || ((item.Kind == SkillBody || item.Kind == Material) && item.Revision == "") {
			return fail(Invalid)
		}
		seen[item.ID] = item.Kind
		packet = append(packet, item)
	}
	superseded := map[string]bool{}
	kinds := map[string]Kind{}
	for _, item := range packet {
		kinds[item.ID] = item.Kind
	}
	for _, item := range packet {
		for _, id := range item.Supersedes {
			if id != item.ID {
				if kind, ok := kinds[id]; ok && dropPriority(kind) == 0 {
					return fail(Invalid)
				}
				superseded[id] = true
			}
		}
	}
	var selected ContextPacket
	for _, item := range packet {
		if superseded[item.ID] {
			report.Dropped = append(report.Dropped, Drop{item.ID, Superseded})
		} else {
			selected = append(selected, item)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return group(selected[i].Kind) < group(selected[j].Kind) })
	build := func() bool {
		r.Items = r.Items[:prefix]
		for _, item := range selected {
			raw, ok := contextMessage(item)
			if !ok {
				return false
			}
			r.Items = append(r.Items, raw)
		}
		r.Items = append(r.Items, append(provider.Item(nil), in.Current...))
		report.Tokens = counter(r)
		return report.Tokens >= 0
	}
	if !build() {
		return fail(Invalid)
	}
	for report.Tokens > in.MaxTokens {
		drop := -1
		for i, item := range selected {
			p := dropPriority(item.Kind)
			if p == 0 {
				continue
			}
			if drop == -1 || p > dropPriority(selected[drop].Kind) || p == dropPriority(selected[drop].Kind) && item.Rank < selected[drop].Rank {
				drop = i
			}
		}
		if drop == -1 {
			return fail(TooLarge)
		}
		report.Dropped = append(report.Dropped, Drop{selected[drop].ID, Budget})
		selected = append(selected[:drop], selected[drop+1:]...)
		if !build() {
			return fail(Invalid)
		}
	}
	for _, item := range selected {
		ref := Ref{item.ID, item.Revision}
		report.Items = append(report.Items, ref)
		if item.Kind == SkillBody {
			report.Skills = append(report.Skills, ref)
		}
	}
	report.Hash = Digest(r)
	return r, report
}
