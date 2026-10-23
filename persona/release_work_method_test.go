// SPDX-License-Identifier: Apache-2.0
package persona_test

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/persona"
)

func releaseSource(t *testing.T) fstest.MapFS {
	t.Helper()
	source := fstest.MapFS{}
	for _, starter := range persona.Starters() {
		for _, name := range []string{"core-v1.md", "default-voice-v1.md", "work-method-v1.json"} {
			path := starter.Key + "/" + name
			data, err := fs.ReadFile(persona.Shipped(), path)
			if err != nil {
				t.Fatal(err)
			}
			source[path] = &fstest.MapFile{Data: data}
		}
	}
	return source
}

func replaceMethodField(t *testing.T, data []byte, field, value string) []byte {
	t.Helper()
	var record map[string]json.RawMessage
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	group, name, nested := strings.Cut(field, ".")
	if nested {
		fields := map[string]json.RawMessage{}
		if raw, ok := record[group]; ok {
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
		}
		fields[name] = json.RawMessage(value)
		record[group], _ = json.Marshal(fields)
	} else {
		record[field] = json.RawMessage(value)
	}
	result, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReleaseWorkMethodsRejectDrift(t *testing.T) {
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			path := starter.Key + "/work-method-v1.json"
			source := releaseSource(t)
			original := source[path].Data
			values := persona.WorkMethodValues(string(original))
			changes := map[string]string{
				"tools.declare_first": `[]`,
				"handoff.owns":        `[]`,
				"handoff.check_back":  `false`,
				"background_runs":     `["reading"]`,
				"reply.candidates":    `2`,
				"reply.comparison":    `"verdict-then-table"`,
				"ask.per_turn":        `2`,
				"ask.form":            `"choices"`,
				"fill":                `["ask.per_turn"]`,
			}
			if starter.Key == "dev" {
				changes["workers.dispatch"] = `false`
			}
			if starter.Key == "life" {
				changes["reply.comparison"] = `"verdict-then-card"`
			}
			if starter.Key == "lina" || starter.Key == "knowledge" {
				changes["ask.form"] = `"narrowed"`
			}
			for field, raw := range values {
				if strings.HasPrefix(field, "done.") {
					var evidence []string
					if err := json.Unmarshal(raw, &evidence); err != nil {
						t.Fatal(err)
					}
					extra := "receipt"
					if field == "done.purchase" {
						extra = "real_view"
					}
					changed, _ := json.Marshal(append(evidence, extra))
					changes[field] = string(changed)
				}
			}
			for field, value := range changes {
				t.Run(field, func(t *testing.T) {
					data := replaceMethodField(t, original, field, value)
					if _, err := persona.ParseWorkMethod(data, starter.Key); err != nil {
						t.Fatal("drift fixture must remain schema-valid", err)
					}
					source[path].Data = data
					if err := persona.Validate(source); err == nil {
						t.Fatal("release accepted a schema-valid change to starter values")
					}
				})
			}
			t.Run("tool order", func(t *testing.T) {
				var groups []string
				if err := json.Unmarshal(values["tools.declare_first"], &groups); err != nil || len(groups) < 2 {
					t.Fatal("expected ordered tool groups", err)
				}
				groups[0], groups[1] = groups[1], groups[0]
				raw, _ := json.Marshal(groups)
				source[path].Data = replaceMethodField(t, original, "tools.declare_first", string(raw))
				if err := persona.Validate(source); err == nil {
					t.Fatal("release accepted reordered tools")
				}
			})
			t.Run("empty record", func(t *testing.T) {
				source[path].Data = []byte(persona.EmptyWorkMethod(starter.Key))
				if err := persona.Validate(source); err == nil {
					t.Fatal("release accepted an empty starter record")
				}
			})
			if len(persona.WorkMethodFillKeys(string(original))) != 0 {
				t.Run("removed fill", func(t *testing.T) {
					source[path].Data = replaceMethodField(t, original, "fill", `[]`)
					if err := persona.Validate(source); err == nil {
						t.Fatal("release accepted removal of fill declarations")
					}
				})
			}
		})
	}
}

func TestReleaseWorkMethodsRequireEveryRecord(t *testing.T) {
	for _, starter := range persona.Starters() {
		for _, mode := range []string{"missing", "invalid"} {
			t.Run(starter.Key+"/"+mode, func(t *testing.T) {
				source := releaseSource(t)
				path := starter.Key + "/work-method-v1.json"
				if mode == "missing" {
					delete(source, path)
				} else {
					source[path].Data = []byte(`{"schema":1,"preset":"` + starter.Key + `","procedure":"private fixture"}`)
				}
				if err := persona.Validate(source); err == nil || !strings.Contains(err.Error(), starter.Key) || strings.Contains(err.Error(), "private fixture") {
					t.Fatalf("release must identify the affected preset without content: %v", err)
				}
			})
		}
	}
}

func TestReleaseWorkMethodsAcceptEquivalentDefaults(t *testing.T) {
	source := releaseSource(t)
	for _, starter := range persona.Starters() {
		path := starter.Key + "/work-method-v1.json"
		data := source[path].Data
		for field, value := range persona.WorkMethodValues(string(data)) {
			data = replaceMethodField(t, data, field, string(value))
		}
		if len(persona.WorkMethodFillKeys(string(data))) == 0 {
			data = replaceMethodField(t, data, "fill", `[]`)
		}
		// JSON escapes spell the same enum and list member values.
		data = []byte(strings.ReplaceAll(string(data), `"choices"`, `"\u0063hoices"`))
		data = []byte(strings.ReplaceAll(string(data), `"reading"`, `"\u0072eading"`))
		source[path].Data = data
	}
	if err := persona.Validate(source); err != nil {
		t.Fatal("explicit defaults or JSON formatting changed release values", err)
	}
}
