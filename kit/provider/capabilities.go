// SPDX-License-Identifier: Apache-2.0
package provider

import (
	"encoding/json"
	"slices"
	"time"
)

// Capability is an optimization, never model entitlement or tool permission.
type Capability string

const (
	PrefixCacheCapability        Capability = "prefix_cache"
	CacheIdentityCapability      Capability = "cache_identity"
	CacheMarksCapability         Capability = "cache_marks"
	CacheLifetime1hCapability    Capability = "cache_lifetime_1h"
	CacheLifetime5mCapability    Capability = "cache_lifetime_5m"
	ReasoningStateCapability     Capability = "reasoning_state"
	ServerStateCapability        Capability = "server_state"
	ProviderCompactionCapability Capability = "provider_compaction"
	BatchCapability              Capability = "batch"
	FlexCapability               Capability = "flex"
)

// CapabilityAdmission reports an adapter configuration's additional narrowing.
// It cannot grant a capability absent from the build's route admission set.
type CapabilityAdmission interface{ AdmittedCapabilities(ModelRef) CapabilitySet }

type CapabilitySet []Capability

func (s CapabilitySet) Has(c Capability) bool { return slices.Contains(s, c) }
func (s CapabilitySet) Valid() bool {
	seen := map[Capability]bool{}
	for _, c := range s {
		switch c {
		case PrefixCacheCapability, CacheIdentityCapability, CacheMarksCapability, CacheLifetime1hCapability, CacheLifetime5mCapability, ReasoningStateCapability, ServerStateCapability, ProviderCompactionCapability, BatchCapability, FlexCapability:
		default:
			return false
		}
		if seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// UsableCacheLifetime reports the cache lifetime both the adapter declaration
// and the route admission accept. A route whose caching declares no lifetime,
// or whose lifetime is not admitted, has no known expiry.
func UsableCacheLifetime(supported, admitted CapabilitySet) (time.Duration, bool) {
	usable := func(c Capability) bool { return supported.Has(c) && admitted.Has(c) }
	switch {
	case usable(CacheLifetime1hCapability):
		return time.Hour, true
	case usable(CacheLifetime5mCapability):
		return 5 * time.Minute, true
	}
	return 0, false
}

// Position is an exclusive prefix count in Items and Tools. Instructions are
// part of the fixed prefix. On-demand tools are excluded from Fixed.Tools.
type Position struct {
	Items int `json:"items"`
	Tools int `json:"tools"`
}
type RequestLayout struct {
	Fixed         Position  `json:"fixed"`
	PreviousInput *Position `json:"previous_input,omitempty"`
	CurrentInput  Position  `json:"current_input"`
	Newest        *Position `json:"newest,omitempty"`
}

func (l RequestLayout) Valid(r Request) bool {
	valid := func(p Position) bool {
		return p.Items >= 0 && p.Items <= len(r.Items) && p.Tools >= 0 && p.Tools <= len(r.Tools)
	}
	if !valid(l.Fixed) || !valid(l.CurrentInput) || l.CurrentInput.Items <= l.Fixed.Items {
		return false
	}
	if l.PreviousInput != nil && (!valid(*l.PreviousInput) || l.PreviousInput.Items <= l.Fixed.Items || l.PreviousInput.Items >= l.CurrentInput.Items) {
		return false
	}
	return l.Newest == nil || valid(*l.Newest) && l.Newest.Items > l.CurrentInput.Items
}

type CacheLifetime string

const (
	Cache1Hour    CacheLifetime = "1h"
	Cache5Minutes CacheLifetime = "5m"
)

type CacheMark struct {
	Position Position      `json:"position"`
	Lifetime CacheLifetime `json:"lifetime,omitempty"`
}

// CapabilityUse contains only selected values, safe to record without request
// bodies. An absent field means the capability was not used.
type CapabilityUse struct {
	PrefixCache        bool        `json:"prefix_cache,omitempty"`
	CacheIdentity      string      `json:"cache_identity,omitempty"`
	CacheMarks         []CacheMark `json:"cache_marks,omitempty"`
	ReasoningState     bool        `json:"reasoning_state,omitempty"`
	ServerState        string      `json:"server_state,omitempty"`
	ProviderCompaction bool        `json:"provider_compaction,omitempty"`
	Batch              string      `json:"batch,omitempty"`
	Flex               bool        `json:"flex,omitempty"`
}

func (u CapabilityUse) Names() CapabilitySet {
	var out CapabilitySet
	if u.PrefixCache {
		out = append(out, PrefixCacheCapability)
	}
	if u.CacheIdentity != "" {
		out = append(out, CacheIdentityCapability)
	}
	if len(u.CacheMarks) > 0 {
		out = append(out, CacheMarksCapability)
	}
	for _, m := range u.CacheMarks {
		switch m.Lifetime {
		case Cache1Hour:
			if !out.Has(CacheLifetime1hCapability) {
				out = append(out, CacheLifetime1hCapability)
			}
		case Cache5Minutes:
			if !out.Has(CacheLifetime5mCapability) {
				out = append(out, CacheLifetime5mCapability)
			}
		}
	}
	if u.ReasoningState {
		out = append(out, ReasoningStateCapability)
	}
	if u.ServerState != "" {
		out = append(out, ServerStateCapability)
	}
	if u.ProviderCompaction {
		out = append(out, ProviderCompactionCapability)
	}
	if u.Batch != "" {
		out = append(out, BatchCapability)
	}
	if u.Flex {
		out = append(out, FlexCapability)
	}
	return out
}
func ValidCacheIdentity(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidateUse rejects requested optimizations before a transport can start.
func ValidateUse(r Request, supported, admitted CapabilitySet) error {
	reject := func() error { return &Failure{Kind: NotAccepted} }
	if !supported.Valid() || !admitted.Valid() {
		return reject()
	}
	for _, c := range r.Capabilities.Names() {
		if !supported.Has(c) || !admitted.Has(c) {
			return reject()
		}
	}
	u := r.Capabilities
	if u.CacheIdentity != "" && (!ValidCacheIdentity(u.CacheIdentity) || u.CacheIdentity != r.CacheIdentity) {
		return reject()
	}
	if len(u.CacheMarks) > 4 {
		return reject()
	}
	if len(u.CacheMarks) > 0 && (r.Layout == nil || !r.Layout.Valid(r)) {
		return reject()
	}
	seen := map[Position]bool{}
	for _, m := range u.CacheMarks {
		if m.Lifetime != "" && m.Lifetime != Cache1Hour && m.Lifetime != Cache5Minutes {
			return reject()
		}
		l := r.Layout
		if m.Position != l.Fixed && m.Position != l.CurrentInput && (l.PreviousInput == nil || m.Position != *l.PreviousInput) && (l.Newest == nil || m.Position != *l.Newest) {
			return reject()
		}
		if seen[m.Position] {
			return reject()
		}
		seen[m.Position] = true
	}
	return nil
}

// CacheUsage retains independently reported lifetime buckets. Nil counts,
// absent buckets and absent lifetime maps all mean unknown, never zero.
type CacheUsage struct {
	Read  *int64 `json:"read,omitempty"`
	Write *int64 `json:"write,omitempty"`
}

func (u CapabilityUse) Empty() bool { return len(u.Names()) == 0 }

// Clone isolates mutable mark slices at the evidence boundary.
func (u CapabilityUse) Clone() CapabilityUse { u.CacheMarks = slices.Clone(u.CacheMarks); return u }
func (l *RequestLayout) Clone() *RequestLayout {
	if l == nil {
		return nil
	}
	out := *l
	if l.PreviousInput != nil {
		p := *l.PreviousInput
		out.PreviousInput = &p
	}
	if l.Newest != nil {
		p := *l.Newest
		out.Newest = &p
	}
	return &out
}

// Keep optional metadata out of legacy digest inputs when it is absent.
func RequestMetadata(r Request) []byte {
	if r.CacheIdentity == "" && r.Layout == nil && r.Capabilities.Empty() {
		return nil
	}
	b, _ := json.Marshal(struct {
		Identity string         `json:"cache_identity,omitempty"`
		Layout   *RequestLayout `json:"layout,omitempty"`
		Used     CapabilityUse  `json:"capabilities"`
	}{r.CacheIdentity, r.Layout, r.Capabilities})
	return b
}
