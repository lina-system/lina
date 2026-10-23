// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

func TestReplayVariantsUseShippedTailoring(t *testing.T) {
	variants, err := replayVariants("")
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 2 || variants[0].Name != "baseline" || variants[1].Name != "tailored" {
		t.Fatalf("variants=%v", variants)
	}
	dev, ok := variants[1].For("dev")
	if !ok || dev.WorkMethod == persona.EmptyWorkMethod("dev") {
		t.Fatalf("tailored dev record missing: %+v", dev)
	}
	before, _ := variants[0].For("dev")
	if before.WorkMethod != persona.EmptyWorkMethod("dev") || before.Layers.Core != dev.Layers.Core {
		t.Fatalf("baseline is not the shipped layers without the record: %+v", before)
	}
	for _, starter := range persona.Starters() {
		if _, ok := variants[1].For(starter.Key); !ok {
			t.Fatalf("tailored variant lacks %s", starter.Key)
		}
	}
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
