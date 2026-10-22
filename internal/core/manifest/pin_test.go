// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work/pin"
)

func TestReadRetainsAndValidatesCRWPin(t *testing.T) {
	r := pin.Record{Version: 1, Repository: pin.Repository, Commit: strings.Repeat("a", 40),
		Domains:  []string{"record-schema"},
		Schemas:  []pin.File{{Path: "schema/relationship.json", Source: "contract/schema/relationship.json", SHA256: strings.Repeat("b", 64)}},
		Fixtures: []pin.File{{Path: "fixtures/records.json.gz", Source: "contract/fixtures/records.json.gz", SHA256: strings.Repeat("c", 64), Domain: "record-schema"}},
	}
	value := validManifest()
	value["crwPin"] = r
	m, err := Read(writeManifest(t, encoded(t, value)))
	if err != nil {
		t.Fatal(err)
	}
	// A read/write must retain the exact pin, including every adopted file.
	var roundTrip map[string]json.RawMessage
	if err := json.Unmarshal(encoded(t, m), &roundTrip); err != nil {
		t.Fatal(err)
	}
	var got pin.Record
	if err := json.Unmarshal(roundTrip["crwPin"], &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Fatalf("lost CRW provenance: %+v", got)
	}
	for _, invalid := range []any{map[string]any{}, "HEAD", map[string]any{"commit": "HEAD"}} {
		value["crwPin"] = invalid
		if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
			t.Fatal("accepted invalid CRW pin")
		}
	}
}
