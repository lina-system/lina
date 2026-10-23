// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/persona"
)

func TestWorkMethodPublication(t *testing.T) {
	for _, mode := range []string{"record-only", "invalid", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			r, _, a, _ := managementFixture(t)
			r.agent = a
			source := fstest.MapFS{
				"lina/core-v1.md":          {Data: []byte("fixture core")},
				"lina/default-voice-v1.md": {Data: []byte("fixture voice")},
				"lina/work-method-v1.json": {Data: []byte(`{"schema":1,"preset":"lina","ask":{"form":"choices"}}`)},
			}
			var warnings bytes.Buffer
			r.presets = newPresetPublisher(source, &warnings)
			if err := r.refreshPersona(t.Context()); err != nil {
				t.Fatal(err)
			}
			before, err := r.agents.Prepare(t.Context(), "before-method", a)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "record-only":
				source["lina/work-method-v1.json"].Data = []byte(`{"schema":1,"preset":"lina","ask":{"form":"narrowed"}}`)
			case "invalid":
				source["lina/core-v1.md"].Data = []byte("must not publish")
				source["lina/work-method-v1.json"].Data = []byte(`{"schema":1,"preset":"lina","unknown":true}`)
			case "oversized":
				source["lina/core-v1.md"].Data = []byte("must not publish")
				source["lina/work-method-v1.json"].Data = []byte(strings.Repeat(" ", 16<<10) + `{"schema":1,"preset":"lina"}`)
			}
			for range 3 {
				if err := r.refreshPersona(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			after, err := r.agents.Prepare(t.Context(), "after-method", a)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "record-only" {
				if after.Revision != before.Revision+1 || after.Commit == before.Commit || after.WorkMethodRevision == before.WorkMethodRevision {
					t.Fatal("record-only publication did not mint exactly one revision")
				}
			} else {
				if after.Commit != before.Commit {
					t.Fatal("invalid record replaced the published revision")
				}
				if strings.Count(warnings.String(), "\n") != 1 || !strings.Contains(warnings.String(), "lina") {
					t.Fatalf("expected one public-key warning: %q", warnings.String())
				}
			}
			pinned, err := r.agents.Prepare(t.Context(), "before-method", a)
			if err != nil || pinned.Commit != before.Commit || pinned.WorkMethodRevision != before.WorkMethodRevision || pinned.WorkMethod != before.WorkMethod {
				t.Fatal("prepared turn changed", err)
			}
			fresh, err := r.agents.Create(t.Context(), agents.Create{Name: "Fresh", PersonID: r.person, Preset: "lina"})
			if err != nil {
				t.Fatal(err)
			}
			freshPin, err := r.agents.Prepare(t.Context(), "fresh-method", fresh.ID)
			if err != nil || freshPin.Layers != after.Layers || freshPin.WorkMethod != after.WorkMethod {
				t.Fatal("preset and agent publication disagree", err)
			}
		})
	}
}

func TestShippedWorkMethodsArePresent(t *testing.T) {
	for _, starter := range persona.Starters() {
		if _, err := fs.ReadFile(persona.Shipped(), starter.Key+"/work-method-v1.json"); err != nil {
			t.Errorf("%s record missing: %v", starter.Key, err)
		}
	}
}

func TestMissingWorkMethodPublishesDefaultsAndWarnsOnce(t *testing.T) {
	r, _, a, _ := managementFixture(t)
	r.agent = a
	source := fstest.MapFS{
		"lina/core-v1.md":          {Data: []byte("core")},
		"lina/default-voice-v1.md": {Data: []byte("voice")},
	}
	var warnings bytes.Buffer
	r.presets = newPresetPublisher(source, &warnings)
	for range 3 {
		if err := r.refreshPersona(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	pin, err := r.agents.Prepare(t.Context(), "missing", a)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := persona.ParseWorkMethod([]byte(persona.EmptyWorkMethod("lina")), "lina")
	if err != nil || pin.WorkMethod != empty || pin.Layers.Core != "core" || pin.Layers.DefaultVoice != "voice" {
		t.Fatal("missing record did not publish the empty triple", pin, err)
	}
	if warnings.String() != persona.WorkMethodWarning("lina", true)+"\n" {
		t.Fatal("missing record warnings repeated", warnings.String())
	}
}
