// SPDX-License-Identifier: Apache-2.0
package codex

import (
	_ "embed"
	"encoding/json"
)

//go:embed versions.json
var verifiedVersionsJSON []byte

// VerifiedVersions records session round trips, not schema extraction or approval
// integration. The build manifest reads the same data without a layer dependency.
func VerifiedVersions() []string {
	var versions []string
	if err := json.Unmarshal(verifiedVersionsJSON, &versions); err != nil {
		panic(err)
	}
	return versions
}
