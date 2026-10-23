// SPDX-License-Identifier: Apache-2.0

package protocol

import "slices"

// ValidateSurfaceBody checks the closed lina.cards 1.0 catalog in the canonical
// protocol schema. Core additionally checks references, bindings and actions.
func ValidateSurfaceBody(raw []byte) error {
	return clientSchema.Defs["surface_body"].Validate(raw, clientSchema)
}

// ValidateSurfaceComponent checks each message before it enters a folded card.
func ValidateSurfaceComponent(raw []byte) error {
	return clientSchema.Defs["surface_component"].Validate(raw, clientSchema)
}

// CapabilityIdentity compares the grant, independently of its render options.
func CapabilityIdentity(a, b Capability) bool { return a.Name == b.Name && a.Version == b.Version }
func HasCapability(caps []Capability, want Capability) bool {
	for _, c := range caps {
		if CapabilityIdentity(c, want) {
			return true
		}
	}
	return false
}
func SurfaceResultsCapability() Capability {
	types := []ResultType{ResultTypeSurface}
	catalogs := []string{"lina.cards/1.0"}
	return Capability{Name: "results", Version: "1.0", Types: &types, Catalogs: &catalogs}
}

// IntersectCapability grants only renderers offered by both peers. Non-results
// capabilities retain their original name/version semantics.
func IntersectCapability(supported, offered Capability) Capability {
	out := Capability{Name: supported.Name, Version: supported.Version}
	if out.Name != "results" {
		return out
	}
	types := []ResultType{}
	catalogs := []string{}
	if supported.Types != nil && offered.Types != nil {
		for _, v := range *supported.Types {
			if slices.Contains(*offered.Types, v) && !slices.Contains(types, v) {
				types = append(types, v)
			}
		}
	}
	if supported.Catalogs != nil && offered.Catalogs != nil {
		for _, v := range *supported.Catalogs {
			if slices.Contains(*offered.Catalogs, v) && !slices.Contains(catalogs, v) {
				catalogs = append(catalogs, v)
			}
		}
	}
	out.Types = &types
	out.Catalogs = &catalogs
	return out
}
func CapabilitySubset(granted, offered Capability) bool {
	if !CapabilityIdentity(granted, offered) {
		return false
	}
	if granted.Types != nil {
		for _, v := range *granted.Types {
			if offered.Types == nil || !slices.Contains(*offered.Types, v) {
				return false
			}
		}
	}
	if granted.Catalogs != nil {
		for _, v := range *granted.Catalogs {
			if offered.Catalogs == nil || !slices.Contains(*offered.Catalogs, v) {
				return false
			}
		}
	}
	return true
}
