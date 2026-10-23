// SPDX-License-Identifier: Apache-2.0
package result_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/result"
)

var examples = map[result.Type]string{
	result.SummaryType:    `{"outline":[{"heading":"First","notes":"Context"}],"key_points":["Point"],"decisions":["Choice"],"open_questions":[{"question":"Why?","answer":""}],"scope":{"includes":["Inside"],"excludes":["Outside"]},"length":2}`,
	result.TableType:      `{"columns":[{"name":"Count","type":"number","unit":"items"},{"name":"Ready","type":"boolean","unit":""}],"rows":[[2,true]],"totals":[2,true]}`,
	result.DiagramType:    `{"kind":"flow","direction":"right","nodes":[{"id":"a","label":"Start"},{"id":"b","label":"End"}],"edges":[{"from":"a","to":"b","label":"Next"}],"groups":[{"label":"Both","nodes":["a","b"]}]}`,
	result.ChartType:      `{"kind":"bar","series":[{"name":"Count","values":[{"x":"A","y":2}]}],"x_axis":{"label":"Category","unit":""},"y_axis":{"label":"Count","unit":"items"}}`,
	result.DraftType:      `{"blocks":[{"kind":"heading","text":"Heading","level":1},{"kind":"paragraph","text":"Paragraph"},{"kind":"list","items":["One","Two"]},{"kind":"code","text":"print(1)","language":"python"},{"kind":"quote","text":"Quotation"},{"kind":"image","asset":{"asset_id":"image","revision":1},"text":"Alt"},{"kind":"citation","anchor":{"event_id":"event","start":0,"end":2},"text":"Evidence"}],"from_summary":{"result_id":"outline","revision":1}}`,
	result.ComparisonType: `{"targets":[{"label":"Document","old":"Before","new":"After","hunks":[{"old_start":0,"old_count":1,"new_start":0,"new_count":1}]}],"apply":null}`,
	result.PlanType:       `{"entries":[{"id":"one","kind":"project","title":"First","state":"active","parent":null},{"id":"two","kind":"todo","title":"Second","state":"planned","parent":"one"}],"confirm":null}`,
	result.FileType:       `{"asset":{"asset_id":"file","revision":1},"name":"Report.pdf","media_type":"application/pdf","size":42}`,
}

func TestValidateAndRenderAllTypes(t *testing.T) {
	for kind, raw := range examples {
		t.Run(string(kind), func(t *testing.T) {
			body, err := result.Validate(kind, json.RawMessage(raw), result.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			text := result.PlainText(body, nil)
			if text == "" || text != result.PlainText(body, nil) {
				t.Fatalf("unstable rendering: %q", text)
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := result.Validate(kind, encoded, result.Limits{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMalformed(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": `{}`, "null": `null`, "unknown": strings.Replace(examples[result.SummaryType], `"length":2`, `"length":2,"html":"<b>text</b>"`, 1),
		"duplicate":        strings.Replace(examples[result.SummaryType], `"length":2`, `"length":2,"length":3`, 1),
		"wrong case":       strings.Replace(examples[result.SummaryType], `"length"`, `"Length"`, 1),
		"null required":    strings.Replace(examples[result.SummaryType], `"key_points":["Point"]`, `"key_points":null`, 1),
		"negative screens": strings.Replace(examples[result.SummaryType], `"length":2`, `"length":-1`, 1),
		"trailing":         examples[result.SummaryType] + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := result.Validate(result.SummaryType, json.RawMessage(raw), result.Limits{}); !errors.Is(err, result.ErrMalformed) {
				t.Fatalf("got %v", err)
			}
		})
	}
	for kind, raw := range examples {
		if _, err := result.Validate(kind, json.RawMessage(raw), result.Limits{Bytes: 8}); !errors.Is(err, result.ErrMalformed) {
			t.Fatalf("size: %s: %v", kind, err)
		}
	}
	if _, err := result.Validate(result.Type("app"), []byte(`{}`), result.Limits{}); !errors.Is(err, result.ErrMalformed) {
		t.Fatal(err)
	}
}

func TestSemanticConstraints(t *testing.T) {
	cases := []struct {
		kind result.Type
		raw  string
	}{
		{result.TableType, strings.Replace(examples[result.TableType], `[2,true]`, `["2",true]`, 1)},
		{result.TableType, strings.Replace(examples[result.TableType], `[2,true]`, `[2]`, 1)},
		{result.DiagramType, strings.Replace(examples[result.DiagramType], `"to":"b"`, `"to":"missing"`, 1)},
		{result.DiagramType, strings.Replace(examples[result.DiagramType], `"id":"b"`, `"id":"a"`, 1)},
		{result.ChartType, strings.Replace(examples[result.ChartType], `"y":2`, `"y":"expression"`, 1)},
		{result.DraftType, `{"blocks":[{"kind":"image","url":"https://example.invalid/image"}],"from_summary":null}`},
		{result.DraftType, `{"blocks":[{"kind":"paragraph","text":"x","level":1}],"from_summary":null}`},
		{result.PlanType, strings.Replace(examples[result.PlanType], `"parent":null`, `"parent":"two"`, 1)},
		{result.FileType, strings.Replace(examples[result.FileType], `"revision":1`, `"revision":0`, 1)},
	}
	for _, c := range cases {
		if _, err := result.Validate(c.kind, []byte(c.raw), result.Limits{}); !errors.Is(err, result.ErrMalformed) {
			t.Errorf("%s accepted %s: %v", c.kind, c.raw, err)
		}
	}
}

func TestRenderingIncludesEveryPart(t *testing.T) {
	body, err := result.Validate(result.SummaryType, []byte(examples[result.SummaryType]), result.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	text := result.PlainText(body, nil)
	for _, part := range []string{"First", "Context", "Point", "Choice", "Why?", "Inside", "Outside", "2"} {
		if !strings.Contains(text, part) {
			t.Errorf("missing %q in %q", part, text)
		}
	}
}

func TestClosedBlockVariantsAndCollectionBounds(t *testing.T) {
	for _, raw := range []string{
		`{"blocks":[{"kind":"paragraph","text":"x","level":0}],"from_summary":null}`,
		`{"blocks":[{"kind":"paragraph","text":"x","asset":null}],"from_summary":null}`,
		`{"blocks":[{"kind":"heading","text":"x"}],"from_summary":null}`,
	} {
		if _, err := result.Validate(result.DraftType, []byte(raw), result.Limits{}); !errors.Is(err, result.ErrMalformed) {
			t.Errorf("accepted %s: %v", raw, err)
		}
	}
	if _, err := result.Validate(result.DiagramType, []byte(examples[result.DiagramType]), result.Limits{Items: 1}); !errors.Is(err, result.ErrMalformed) {
		t.Fatal(err)
	}
}

func TestRemoteArtifactFileMediaTypes(t *testing.T) {
	for _, media := range []string{"application/octet-stream", "image/png", "application/pdf", "application/json"} {
		raw := strings.Replace(examples[result.FileType], "application/pdf", media, 1)
		if _, err := result.Validate(result.FileType, []byte(raw), result.Limits{}); err != nil {
			t.Errorf("%s: %v", media, err)
		}
	}
	for _, media := range []string{"invalid", "image/png;execute=yes", "text/html\r\nX: value"} {
		raw, _ := json.Marshal(result.File{Asset: result.AssetRef{ID: "asset", Revision: 1}, Name: "Artifact", MediaType: media, Size: 1})
		if _, err := result.Validate(result.FileType, raw, result.Limits{}); err == nil {
			t.Errorf("invalid MIME accepted: %q", media)
		}
	}
}
