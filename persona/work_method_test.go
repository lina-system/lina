// SPDX-License-Identifier: Apache-2.0
package persona_test

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/persona"
)

func TestWorkMethodSchema(t *testing.T) {
	for name, suffix := range map[string]string{
		"unknown field":     `,"procedure":"instructions"`,
		"unknown nested":    `,"ask":{"extra":true}`,
		"unknown group":     `,"tools":{"declare_first":["unregistered"]}`,
		"unknown evidence":  `,"done":{"code":["guessed"]}`,
		"unknown work":      `,"done":{"extra":["receipt"]}`,
		"weakened evidence": `,"done":{"code":["receipt"]}`,
		"unknown fill":      `,"fill":["reply.length"]`,
		"metadata fill":     `,"fill":["preset"]`,
		"zero":              `,"ask":{"per_turn":0}`,
		"fraction":          `,"reply":{"candidates":1.5}`,
		"unknown choice":    `,"ask":{"form":"open"}`,
		"null":              `,"workers":{"dispatch":null}`,
		"nested duplicate":  `,"ask":{"form":"choices","form":"narrowed"}`,
		"duplicate schema":  `,"schema":1`,
		"duplicate list":    `,"background_runs":["reading","reading"]`,
		"wrong list type":   `,"fill":"ask.form"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := persona.ParseWorkMethod([]byte(`{"schema":1,"preset":"dev"`+suffix+`}`), "dev"); err == nil {
				t.Fatal("accepted invalid record")
			}
		})
	}
	for _, raw := range []string{`{}`, `null`, `[]`, `{"schema":2,"preset":"dev"}`, `{"schema":1,"preset":"unknown"}`, `{"schema":1,"preset":"lina"}`, `{"schema":1,"preset":"dev"} {}`, string([]byte{0xff})} {
		if _, err := persona.ParseWorkMethod([]byte(raw), "dev"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := persona.ParseWorkMethod([]byte(`{"schema":1,"preset":"lina","workers":{"dispatch":true}}`), "lina"); err == nil {
		t.Fatal("non-dev dispatch accepted")
	}
	for _, s := range persona.Starters() {
		record, missing, err := persona.LoadWorkMethod(persona.Shipped(), s.Key)
		if err != nil || missing {
			t.Fatal(s.Key, err)
		}
		values := persona.WorkMethodValues(record)
		if string(values["done.code"]) != `["owner_path_run"]` || string(values["handoff.check_back"]) != "true" {
			t.Fatalf("defaults missing: %s", s.Key)
		}
	}
	if err := persona.Validate(persona.Shipped()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkMethodLoadingBoundsAndCanonicalization(t *testing.T) {
	raw := `{"schema":1,"preset":"dev","reply":{"candidates":3},"fill":["reply.candidates"]}`
	canonical, err := persona.ParseWorkMethod([]byte(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	pretty := "\n" + strings.ReplaceAll(raw, ",", ",\n") + "\n"
	same, err := persona.ParseWorkMethod([]byte(pretty), "dev")
	if err != nil || same != canonical {
		t.Fatal("format minted another record", err)
	}
	if _, err := persona.ParseWorkMethod([]byte(raw+strings.Repeat(" ", persona.MaxWorkMethodBytes-len(raw))), "dev"); err != nil {
		t.Fatal("boundary rejected", err)
	}
	if _, err := persona.ParseWorkMethod([]byte(raw+strings.Repeat(" ", persona.MaxWorkMethodBytes-len(raw)+1)), "dev"); err == nil {
		t.Fatal("oversized record accepted")
	}
	if _, err := persona.FillWorkMethod(canonical, "reply.candidates", json.RawMessage(`2`)); err != nil {
		t.Fatal(err)
	}
	if _, err := persona.FillWorkMethod(canonical, "ask.form", json.RawMessage(`"choices"`)); err == nil {
		t.Fatal("undeclared fill accepted")
	}
	if _, err := persona.FillWorkMethod(canonical, "reply.candidates", json.RawMessage(`"two"`)); err == nil {
		t.Fatal("invalid typed fill accepted")
	}
	record, missing, err := persona.LoadWorkMethod(fstest.MapFS{}, "dev")
	if err != nil || !missing || record != persona.EmptyWorkMethod("dev") {
		t.Fatal(record, missing, err)
	}
}
