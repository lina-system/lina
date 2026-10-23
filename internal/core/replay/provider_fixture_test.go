// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"fmt"
	"os"
	"strings"
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
	if f.Summary == nil || f.Summary.Segment != 40 {
		t.Fatalf("fixture lost its summary segment: %+v", f.Summary)
	}
}

func TestFixtureSummarySegmentMustEndACompleteTurn(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/provider-replay/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range []int{0, 3, 80, 81} {
		edited := strings.Replace(string(raw), `"segment": 40`, fmt.Sprintf(`"segment": %d`, segment), 1)
		if _, err := ParseProviderFixture(edited); err == nil {
			t.Fatalf("accepted summary segment %d", segment)
		}
	}
}
