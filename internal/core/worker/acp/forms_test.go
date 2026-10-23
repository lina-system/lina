// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestFormPreservesTypesAndValidatesBeforeAnswer(t *testing.T) {
	raw := json.RawMessage(`{"mode":"form","message":"Settings","requestedSchema":{"type":"object","properties":{"label":{"type":"string","minLength":2},"count":{"type":"integer","minimum":1},"enabled":{"type":"boolean"},"color":{"type":"string","enum":["blue","green"]},"tags":{"type":"array","items":{"type":"string","enum":["a","b"]},"minItems":1}},"required":["label","count","enabled","color","tags"]}}`)
	form, err := ParseForm(raw)
	if err != nil {
		t.Fatal(err)
	}
	var values protocol.FormValues
	if err = json.Unmarshal([]byte(`{"label":"ok","count":2,"enabled":true,"color":"blue","tags":"[\"a\"]"}`), &values); err != nil {
		t.Fatal(err)
	}
	answer := protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	reply, err := form.Reply(answer)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Content map[string]json.RawMessage }
	if err = json.Unmarshal(reply, &got); err != nil || string(got.Content["tags"]) != `["a"]` || string(got.Content["count"]) != "2" || string(got.Content["enabled"]) != "true" {
		t.Fatalf("types changed: %s %v", reply, err)
	}
	for field, bad := range map[string]string{"label": `"x"`, "count": "1.5", "enabled": `"true"`, "color": `"red"`, "tags": `"[\"z\"]"`} {
		t.Run(field, func(t *testing.T) {
			old := values[field]
			defer func() { values[field] = old }()
			var v protocol.FormValue
			if err := json.Unmarshal([]byte(bad), &v); err != nil {
				t.Fatal(err)
			}
			values[field] = v
			if _, err := form.Reply(answer); err == nil {
				t.Fatal("invalid form value accepted")
			}
		})
	}
	for _, action := range []protocol.QuestionAnswerAction{protocol.QuestionAnswerActionDecline, protocol.QuestionAnswerActionCancel} {
		reply, err := form.Reply(protocol.QuestionAnswer{Answer: action})
		if err != nil || strings.Contains(string(reply), "content") {
			t.Fatal("nonaccept carried content", string(reply), err)
		}
	}
}
func TestRefusedFieldNamesAreNotProjected(t *testing.T) {
	for _, name := range []string{"api_key", "api-key", "API Key", "token", "password", "credit_card"} {
		raw := json.RawMessage(`{"mode":"form","requestedSchema":{"type":"object","properties":{"` + name + `":{"type":"string"}}}}`)
		if _, err := ParseForm(raw); err == nil {
			t.Fatal("refused field projected", name)
		}
	}
}

func TestOptionalFieldsStayOmittedAndMultiSelectShowsChoices(t *testing.T) {
	raw := json.RawMessage(`{"mode":"form","message":"Settings","requestedSchema":{"type":"object","properties":{"label":{"type":"string","minLength":2},"limit":{"type":"integer"},"tags":{"type":"array","title":"Tags","items":{"type":"string","enum":["a","b"]}},"themes":{"type":"array","items":{"anyOf":[{"const":"dark","title":"Dark"},{"const":"light","title":"Light"}]}}},"required":["label"]}}`)
	form, err := ParseForm(raw)
	if err != nil {
		t.Fatal(err)
	}
	optional := map[string]bool{}
	for _, field := range form.Fields {
		optional[field.Name] = field.Optional != nil && *field.Optional
	}
	if !optional["limit"] || !optional["tags"] || !optional["themes"] || optional["label"] {
		t.Fatalf("required distinction lost: %+v", form.Fields)
	}
	var shown string
	for _, target := range form.Targets {
		if target.Name == "tags" || target.Name == "themes" {
			shown += target.Value + "\n"
		}
	}
	for _, want := range []string{"a", "b", "dark", "Dark", "light", "Light"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("question omitted %q from %s", want, shown)
		}
	}
	label := "ok"
	values := protocol.FormValues{"label": {String: &label}}
	reply, err := form.Reply(protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionAccept, Values: &values})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reply), "limit") || strings.Contains(string(reply), "tags") || !strings.Contains(string(reply), `"label":"ok"`) {
		t.Fatalf("optional values were invented: %s", reply)
	}
}

func TestSecretAndUnsupportedFormsAreRefused(t *testing.T) {
	for _, property := range []string{`{"type":"string","format":"password"}`, `{"type":"string","isSecret":true}`, `{"type":"string","writeOnly":true}`, `{"type":"object"}`, `{"type":"array","items":{"$ref":"file:///not-readable"}}`} {
		raw := json.RawMessage(`{"mode":"form","requestedSchema":{"type":"object","properties":{"value":` + property + `}}}`)
		if _, err := ParseForm(raw); err == nil {
			t.Fatal("unsupported form accepted", property)
		}
	}
}
