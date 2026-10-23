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
func TestSecretAndUnsupportedFormsAreRefused(t *testing.T) {
	for _, property := range []string{`{"type":"string","format":"password"}`, `{"type":"string","isSecret":true}`, `{"type":"string","writeOnly":true}`, `{"type":"object"}`, `{"type":"array","items":{"$ref":"file:///not-readable"}}`} {
		raw := json.RawMessage(`{"mode":"form","requestedSchema":{"type":"object","properties":{"value":` + property + `}}}`)
		if _, err := ParseForm(raw); err == nil {
			t.Fatal("unsupported form accepted", property)
		}
	}
}
