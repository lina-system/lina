// SPDX-License-Identifier: Apache-2.0
package provider

import (
	_ "embed"
	"encoding/json"
)

//go:embed capabilities.json
var routeCapabilities []byte

// Admitted is the build's acceptance policy, also copied into the build manifest.
// Unknown routes have no capabilities. Returned sets cannot mutate that policy.
func Admitted(route string) CapabilitySet {
	var routes map[string]CapabilitySet
	if json.Unmarshal(routeCapabilities, &routes) != nil {
		return nil
	}
	return routes[route]
}
