// SPDX-License-Identifier: Apache-2.0

package crwpin

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func validRecord() Record {
	return Record{
		Version:    1,
		Repository: Repository,
		Commit:     strings.Repeat("a", 40),
		Domains:    []string{"records"},
		Schemas: []File{{
			Path:   "schema/relationship.json",
			Source: "contract/schema/relationship.json",
			SHA256: strings.Repeat("b", 64),
		}},
		Fixtures: []File{{
			Path:   "fixtures/records.json",
			Source: "contract/fixtures/records.json",
			SHA256: strings.Repeat("c", 64),
			Domain: "records",
		}},
	}
}

func TestRecordValidate(t *testing.T) {
	// Empty schemas, fixtures, and domains share one early refusal. A listed
	// domain with no fixture fails later, so those errors must differ.
	cases := []struct {
		name   string
		accept bool
		early  bool
		mutate func(*Record)
	}{
		{name: "valid", accept: true},
		{name: "valid replay", accept: true, mutate: func(r *Record) {
			r.Replay = &ReplayFile{Path: "replay/input.json", SHA256: strings.Repeat("d", 64)}
		}},
		{name: "wrong version", mutate: func(r *Record) { r.Version = 2 }},
		{name: "wrong repository", mutate: func(r *Record) { r.Repository = "https://example.invalid/other" }},
		{name: "short commit", mutate: func(r *Record) { r.Commit = strings.Repeat("a", 39) }},
		{name: "empty schemas", early: true, mutate: func(r *Record) { r.Schemas = nil }},
		{name: "empty fixtures", early: true, mutate: func(r *Record) { r.Fixtures = nil }},
		{name: "empty domains", early: true, mutate: func(r *Record) { r.Domains = nil }},
		{name: "duplicate domain", mutate: func(r *Record) { r.Domains = []string{"records", "records"} }},
		{name: "invalid domain", mutate: func(r *Record) {
			r.Domains = []string{"Records"}
			r.Fixtures[0].Domain = "Records"
		}},
		{name: "duplicate path", mutate: func(r *Record) { r.Fixtures[0].Path = r.Schemas[0].Path }},
		{name: "duplicate source", mutate: func(r *Record) { r.Fixtures[0].Source = r.Schemas[0].Source }},
		{name: "path with ..", mutate: func(r *Record) { r.Schemas[0].Path = "schema/../relationship.json" }},
		{name: "path with backslash", mutate: func(r *Record) { r.Schemas[0].Source = `contract\schema\relationship.json` }},
		{name: "path with colon", mutate: func(r *Record) { r.Schemas[0].Path = "schema:relationship.json" }},
		{name: "unadopted domain", mutate: func(r *Record) {
			r.Fixtures = append(r.Fixtures, File{
				Path:   "fixtures/other.json",
				Source: "contract/fixtures/other.json",
				SHA256: strings.Repeat("d", 64),
				Domain: "other",
			})
		}},
		{name: "schema names domain", mutate: func(r *Record) { r.Schemas[0].Domain = "records" }},
		{name: "empty domain", mutate: func(r *Record) { r.Domains = append(r.Domains, "unused") }},
		{name: "invalid replay path", mutate: func(r *Record) {
			r.Replay = &ReplayFile{Path: "../replay.json", SHA256: strings.Repeat("d", 64)}
		}},
		{name: "invalid replay digest", mutate: func(r *Record) {
			r.Replay = &ReplayFile{Path: "replay/input.json", SHA256: "abcd"}
		}},
		{name: "colliding replay", mutate: func(r *Record) {
			r.Replay = &ReplayFile{Path: r.Schemas[0].Path, SHA256: strings.Repeat("d", 64)}
		}},
	}
	var early, unused string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRecord()
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			err := r.Validate()
			if tc.accept {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted invalid record")
			}
			if tc.early {
				if early == "" {
					early = err.Error()
				} else if err.Error() != early {
					t.Fatalf("error %q, want the shared empty-collection refusal %q", err, early)
				}
			}
			if tc.name == "empty domain" {
				unused = err.Error()
			}
		})
	}
	if early == "" || unused == "" || early == unused {
		t.Fatalf("empty collections %q and unused domain %q were not distinguished", early, unused)
	}
}

func TestRecordVerify(t *testing.T) {
	schema := []byte(`{"type":"object"}`)
	fixture := []byte("[]")
	replay := []byte(`{"placeholder":true}`)
	sum := func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
	record := validRecord()
	record.Schemas[0].SHA256 = sum(schema)
	record.Fixtures[0].SHA256 = sum(fixture)
	record.Replay = &ReplayFile{Path: "replay/input.json", SHA256: sum(replay)}
	match := fstest.MapFS{
		record.Schemas[0].Path:  {Data: schema},
		record.Fixtures[0].Path: {Data: fixture},
		record.Replay.Path:      {Data: replay},
	}

	t.Run("matching bytes", func(t *testing.T) {
		if err := record.Verify(match); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		corpus := fstest.MapFS{
			record.Fixtures[0].Path: {Data: fixture},
			record.Replay.Path:      {Data: replay},
		}
		err := record.Verify(corpus)
		if err == nil || !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), record.Schemas[0].Path) {
			t.Fatalf("missing schema: %v", err)
		}
	})
	t.Run("one-byte change", func(t *testing.T) {
		changed := bytes.Clone(schema)
		changed[0]++
		corpus := fstest.MapFS{
			record.Schemas[0].Path:  {Data: changed},
			record.Fixtures[0].Path: {Data: fixture},
			record.Replay.Path:      {Data: replay},
		}
		err := record.Verify(corpus)
		msg := errString(err)
		actual, want := sum(changed), sum(schema)
		if err == nil || !strings.Contains(msg, record.Schemas[0].Path) || !strings.Contains(msg, actual) || !strings.Contains(msg, want) {
			t.Fatalf("error %q lacks path or hashes %s %s", msg, actual, want)
		}
	})
	t.Run("replay file", func(t *testing.T) {
		changed := bytes.Clone(replay)
		changed[0]++
		corpus := fstest.MapFS{
			record.Schemas[0].Path:  {Data: schema},
			record.Fixtures[0].Path: {Data: fixture},
			record.Replay.Path:      {Data: changed},
		}
		err := record.Verify(corpus)
		msg := errString(err)
		actual, want := sum(changed), sum(replay)
		if err == nil || !strings.Contains(msg, record.Replay.Path) || !strings.Contains(msg, actual) || !strings.Contains(msg, want) {
			t.Fatalf("error %q lacks replay path or hashes %s %s", msg, actual, want)
		}
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
