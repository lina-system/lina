// SPDX-License-Identifier: Apache-2.0
package update

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

func TestPeerRowsRequireProtocolPlatformAndCapabilities(t *testing.T) {
	required := manifest.ClientCombinations()
	for _, change := range []string{"unchanged", "missing", "protocol", "platform", "capability", "evidence"} {
		t.Run(change, func(t *testing.T) {
			rows := manifest.ClientCombinations()
			switch change {
			case "missing":
				rows = nil
			case "protocol":
				rows[0].ProtocolVersion = "9.0"
			case "platform":
				rows[0].Platform = "unknown/unknown"
			case "capability":
				rows[0].Capabilities = rows[0].Capabilities[:1]
			case "evidence":
				rows[0].Evidence = nil
			}
			err := CheckPeers(rows, required)
			if (err == nil) != (change == "unchanged") {
				t.Fatalf("compatibility: %v", err)
			}
		})
	}
}
