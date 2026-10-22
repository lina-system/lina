// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func testRecord() (Record, fstest.MapFS) {
	files := fstest.MapFS{
		"schema/relationship.json": {Data: []byte(`{"type":"object"}`)},
		"fixtures/records.json.gz": {Data: []byte("fixture bytes")},
	}
	return Record{Version: 1, Repository: Repository, Commit: strings.Repeat("a", 40),
		Domains:  []string{"record-schema"},
		Schemas:  []File{{Path: "schema/relationship.json", Source: "contract/schema/relationship.json", SHA256: fmt.Sprintf("%x", sha256.Sum256(files["schema/relationship.json"].Data))}},
		Fixtures: []File{{Path: "fixtures/records.json.gz", Source: "contract/fixtures/records.json.gz", SHA256: fmt.Sprintf("%x", sha256.Sum256(files["fixtures/records.json.gz"].Data)), Domain: "record-schema"}},
	}, files
}

func TestVerifyExactBytes(t *testing.T) {
	record, files := testRecord()
	if err := record.Verify(files); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		t.Run(name, func(t *testing.T) {
			r, f := testRecord()
			f[name].Data = append(f[name].Data, '\n')
			if err := r.Verify(f); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("changed bytes accepted or unidentified: %v", err)
			}
			delete(f, name)
			if err := r.Verify(f); err == nil {
				t.Fatal("missing pinned file accepted")
			}
		})
	}
}

func TestRejectInvalidPin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Record)
	}{
		{"commit", func(r *Record) { r.Commit = "HEAD" }},
		{"source", func(r *Record) { r.Repository = "https://example.invalid" }},
		{"version", func(r *Record) { r.Version++ }},
		{"no schemas", func(r *Record) { r.Schemas = nil }},
		{"no fixtures", func(r *Record) { r.Fixtures = nil }},
		{"no domains", func(r *Record) { r.Domains = nil }},
		{"unused domain", func(r *Record) { r.Domains = append(r.Domains, "unused") }},
		{"unadopted domain", func(r *Record) { r.Fixtures[0].Domain = "other" }},
		{"duplicate domain", func(r *Record) { r.Domains = append(r.Domains, r.Domains[0]) }},
		{"duplicate path", func(r *Record) { r.Schemas = append(r.Schemas, r.Schemas[0]) }},
		{"duplicate source", func(r *Record) { f := r.Schemas[0]; f.Path = "schema/other.json"; r.Schemas = append(r.Schemas, f) }},
		{"escape", func(r *Record) { r.Schemas[0].Path = "../schema.json" }},
		{"source escape", func(r *Record) { r.Schemas[0].Source = "../schema.json" }},
		{"hash", func(r *Record) { r.Schemas[0].SHA256 = strings.Repeat("z", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := testRecord()
			tc.change(&r)
			if err := r.Validate(); err == nil {
				t.Fatal("invalid pin accepted")
			}
		})
	}
}
