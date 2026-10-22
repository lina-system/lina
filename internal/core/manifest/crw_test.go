// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work/pin"
)

func TestCRWBuildRunsThePinnedCorpus(t *testing.T) {
	root := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	schema := []byte(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`)
	fixture := []byte(`[{"schema":"relationship","label":"object","instance":"{}","errors":[]},
		{"schema":"relationship","label":"not object","instance":"false","errors":["not an object"]}]`)
	r := pin.Record{Version: 1, Repository: pin.Repository, Commit: strings.Repeat("a", 40),
		Domains:  []string{"record-schema"},
		Schemas:  []pin.File{{Path: "schema/relationship.json", Source: "contract/schema/relationship.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(schema))}},
		Fixtures: []pin.File{{Path: "fixtures/records.json", Source: "contract/fixtures/records.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(fixture)), Domain: "record-schema"}},
	}
	write("testdata/crw/schema/relationship.json", schema)
	write("testdata/crw/fixtures/records.json", fixture)
	write("internal/core/work/pin/pin.json", encoded(t, r))
	got, err := pin.Build(root)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("pin=%+v error=%v", got, err)
	}
	write("testdata/crw/schema/relationship.json", []byte(`true`))
	if _, err := pin.Build(root); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("changed hash: %v", err)
	}
	// Updating a schema digest alone does not make an incompatible implementation
	// agree with the fixture's independently recorded negative judgment.
	r.Schemas[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(`true`)))
	write("internal/core/work/pin/pin.json", encoded(t, r))
	if _, err := pin.Build(root); err == nil || !strings.Contains(err.Error(), "fixture expects false") {
		t.Fatalf("changed behavior: %v", err)
	}
	write("internal/core/work/pin/pin.json", append(encoded(t, r), []byte(` {}`)...))
	if _, err := pin.Build(root); err == nil {
		t.Fatal("trailing pin data accepted")
	}
	if err := os.Remove(filepath.Join(root, "internal/core/work/pin/pin.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := pin.Build(root); err == nil {
		t.Fatal("missing pin accepted")
	}
}
