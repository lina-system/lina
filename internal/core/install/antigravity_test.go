// SPDX-License-Identifier: Apache-2.0
package install

import "testing"

func TestAntigravityDiscoveryUsesInstalledAgy(t *testing.T) {
	host := &fakeHost{versions: map[string]string{"agy": "agy 1.2.16"}}
	for _, tool := range discover(t.Context(), Config{}, host, nil) {
		if tool.Name == "antigravity" {
			if tool.Path != "/tools/agy" || tool.Version != "agy 1.2.16" || tool.Status != "unverified" {
				t.Fatalf("installed Antigravity missed: %+v", tool)
			}
			return
		}
	}
	t.Fatal("Antigravity tool row missing")
}
