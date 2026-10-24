// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

// The record-alone comparison keeps the shipped layers on both sides and strips
// only the record from the baseline, so it measures the record's own
// contribution. The shipped records carry the mechanism fields (declared tool
// groups, worker dispatch, work-kind ownership); the reply-shaping values of
// M4-3 are not shipped, because the M4-4 replay measured them and no agent
// improved without another regressing.
func TestReplayVariantsUseTheShippedTriple(t *testing.T) {
	variants, err := replayVariants("")
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 2 || variants[0].Name != "baseline" || variants[1].Name != "tailored" {
		t.Fatalf("variants=%v", variants)
	}
	for _, starter := range persona.Starters() {
		tailored, ok := variants[1].For(starter.Key)
		if !ok {
			t.Fatalf("tailored variant lacks %s", starter.Key)
		}
		before, ok := variants[0].For(starter.Key)
		if !ok {
			t.Fatalf("baseline variant lacks %s", starter.Key)
		}
		if !sameMethod(t, before.WorkMethod, persona.EmptyWorkMethod(starter.Key), starter.Key) {
			t.Fatalf("%s: baseline is not the empty record: %s", starter.Key, before.WorkMethod)
		}
		if before.Layers != tailored.Layers {
			t.Fatalf("%s: baseline layers differ from the shipped layers", starter.Key)
		}
	}
}

// sameMethod compares two records by parsed value: canonical JSON orders object
// keys differently from the record literal a preset writes.
func sameMethod(t *testing.T, a, b, key string) bool {
	t.Helper()
	var left, right map[string]any
	first, err := persona.ParseWorkMethod([]byte(a), key)
	if err != nil {
		t.Fatal(err)
	}
	second, err := persona.ParseWorkMethod([]byte(b), key)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(first), &left) != nil || json.Unmarshal([]byte(second), &right) != nil {
		t.Fatal("record is not an object")
	}
	return reflect.DeepEqual(left, right)
}

func TestReplayVariantsReadASnapshot(t *testing.T) {
	dir := t.TempDir()
	for _, starter := range persona.Starters() {
		if err := os.MkdirAll(filepath.Join(dir, starter.Key), 0700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"core-v1.md":          "old core " + starter.Key,
			"default-voice-v1.md": "old voice " + starter.Key,
			"work-method-v1.json": `{"schema":1,"preset":"` + starter.Key + `","reply":{"candidates":2}}`,
		} {
			if err := os.WriteFile(filepath.Join(dir, starter.Key, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	variants, err := replayVariants(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := variants[0].For("dev")
	if !ok || before.Layers.Core != "old core dev" {
		t.Fatalf("snapshot not read: %+v", before)
	}
	var record struct{ Reply struct{ Candidates int } }
	if err := json.Unmarshal([]byte(before.WorkMethod), &record); err != nil || record.Reply.Candidates != 2 {
		t.Fatalf("snapshot record=%s error=%v", before.WorkMethod, err)
	}
}

func TestReplayVariantsRefuseAMissingSnapshot(t *testing.T) {
	if _, err := replayVariants(t.TempDir()); err == nil {
		t.Fatal("empty snapshot accepted")
	}
}

func TestBaselineKind(t *testing.T) {
	if baselineKind("") != "shipped-layers-without-record" || baselineKind("/x") != "snapshot" {
		t.Fatal("baseline kind mislabelled")
	}
}
