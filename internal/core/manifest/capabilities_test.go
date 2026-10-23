// SPDX-License-Identifier: Apache-2.0
package manifest

import "testing"

func TestManifestRejectsUnadmittedRouteCapabilities(t *testing.T) {
	value := validManifest()
	value["providerCapabilities"] = map[string]any{"chatgpt": []string{"server_state"}}
	if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
		t.Fatal("unadmitted server state accepted")
	}
}
