// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"os"
	"testing"
)

func TestPublicProviderFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/provider-replay/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseProviderFixture(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tools) < 40 {
		t.Fatalf("missing LINA declarations: %d", len(f.Tools))
	}

}
