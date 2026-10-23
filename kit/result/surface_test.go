// SPDX-License-Identifier: Apache-2.0
package result

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func surfaceFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSurfaceResearchCards(t *testing.T) {
	for name, elements := range map[string][]string{
		"choice":   {"치과 예약 시간", "되는 시간", "[1]", "화 10:30", "[2]", "수 15:00", "[3]", "금 09:00", "[a]", "이 시간으로 예약 요청"},
		"progress": {"진행 중인 일", "자료 조사", "running", "2분", "초안 작성", "waiting", "1분", "검토", "done", "3분", "[a]", "[b]", "[c]"},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := Validate(Type("surface"), surfaceFixture(t, name), Limits{})
			if err != nil {
				t.Fatalf("research card refused: %v", err)
			}
			text := PlainText(body, nil)
			for _, element := range elements {
				if !strings.Contains(text, element) {
					t.Errorf("missing %q in %q", element, text)
				}
			}
		})
	}
}

func TestSurfaceRejectsCatalogAndGraphFailures(t *testing.T) {
	raw := string(surfaceFixture(t, "choice"))
	for name, change := range map[string][2]string{
		"widget":          {"\"ChoicePicker\"", "\"Image\""},
		"field":           {"\"component\":\"Text\"", "\"color\":\"red\",\"component\":\"Text\""},
		"catalog":         {"\"lina.cards\"", "\"other\""},
		"catalog version": {"\"catalogVersion\": \"1.0\"", "\"catalogVersion\": \"2.0\""},
		"wire version":    {"\"v0.9\"", "\"v0.9.1\""},
		"missing child":   {"\"bookLabel\",\"action\"", "\"missing\",\"action\""},
		"cycle":           {"[\"title\",\"slots\",\"book\"]", "[\"root\"]"},
		"binding":         {"\"/slot\"", "\"/missing\""},
	} {
		t.Run(name, func(t *testing.T) {
			broken := strings.Replace(raw, change[0], change[1], 1)
			if broken == raw {
				t.Fatal("mutation did not apply")
			}
			if _, err := Validate(Type("surface"), []byte(broken), Limits{}); !errors.Is(err, ErrSurfaceValidation) {
				t.Fatalf("accepted invalid card: %v", err)
			}
		})
	}
}

func TestSurfaceFoldingAtomicRevision(t *testing.T) {
	var fixture Surface
	if err := json.Unmarshal(surfaceFixture(t, "choice"), &fixture); err != nil {
		t.Fatal(err)
	}
	components, _ := json.Marshal(fixture.Components)
	data, _ := json.Marshal(fixture.Data)
	batch := SurfaceBatch{Messages: []json.RawMessage{
		json.RawMessage(`{"version":"v0.9","createSurface":{"surfaceId":"dentist","catalogId":"lina.cards"}}`),
		json.RawMessage(`{"version":"v0.9","updateComponents":{"surfaceId":"dentist","components":` + string(components) + `}}`),
		json.RawMessage(`{"version":"v0.9","updateDataModel":{"surfaceId":"dentist","value":` + string(data) + `}}`),
	}}
	s, err := FoldSurface(nil, batch)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s)
	update := SurfaceBatch{Messages: []json.RawMessage{json.RawMessage(`{"version":"v0.9","updateDataModel":{"surfaceId":"dentist","path":"/slot","value":["wed-1500"]}}`)}}
	next, err := FoldSurface(s, update)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next.plainText(), "● 수 15:00") || strings.Contains(s.plainText(), "● 수 15:00") {
		t.Fatal("fold mutated original or missed update")
	}
	update.Messages = append(update.Messages, json.RawMessage(`{"version":"v0.9","updateComponents":{"surfaceId":"dentist","components":[{"id":"bad","component":"Image"}]}}`))
	if _, err := FoldSurface(s, update); err == nil {
		t.Fatal("invalid tail accepted")
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("failed batch mutated card")
	}
	deleted, err := FoldSurface(s, SurfaceBatch{Messages: []json.RawMessage{json.RawMessage(`{"version":"v0.9","deleteSurface":{"surfaceId":"dentist"}}`)}})
	if err != nil || !deleted.Deleted || len(deleted.Actions()) != 0 {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
}

func TestSurfaceActionSchema(t *testing.T) {
	s, err := validateSurface(surfaceFixture(t, "choice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"wed-1500"`, `["wed-1500"]`} {
		got, err := s.ValidateAction("choose", map[string]json.RawMessage{"slot": json.RawMessage(value)})
		if err != nil || string(got["slot"]) != `"wed-1500"` {
			t.Fatalf("single choice: %s %v", got, err)
		}
	}
	for _, values := range []map[string]json.RawMessage{
		{"slot": json.RawMessage(`"unknown"`)}, {"slot": json.RawMessage(`["tue-1030","wed-1500"]`)},
		{"slot": json.RawMessage(`true`)}, {"slot": json.RawMessage(`"wed-1500"`), "approval": json.RawMessage(`true`)}, {},
	} {
		if _, err := s.ValidateAction("choose", values); err == nil {
			t.Fatalf("accepted %s", values)
		}
	}
	if _, err := s.ValidateAction("approve", map[string]json.RawMessage{}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestSurfaceDataPointerArrayUpdate(t *testing.T) {
	previous, err := validateSurface(surfaceFixture(t, "choice"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := FoldSurface(previous, SurfaceBatch{Messages: []json.RawMessage{json.RawMessage(`{"version":"v0.9","updateDataModel":{"surfaceId":"dentist","path":"/slot/0","value":"wed-1500"}}`)}})
	if err != nil || !strings.Contains(next.plainText(), "● 수 15:00") {
		t.Fatalf("array update: %+v %v", next, err)
	}
}

func TestSurfaceBindingsRefuseWrongTypes(t *testing.T) {
	raw := string(surfaceFixture(t, "choice"))
	for _, value := range []string{`null`, `"tue-1030"`, `true`, `[42]`} {
		bad := strings.Replace(raw, `"slot":["tue-1030"]`, `"slot":`+value, 1)
		if _, err := Validate(SurfaceType, []byte(bad), Limits{}); err == nil {
			t.Fatalf("accepted bound choice %s", value)
		}
	}
}

func TestSurfaceFormCatalogAndActionTypes(t *testing.T) {
	raw := []byte(`{"version":"v0.9","catalogId":"lina.cards","catalogVersion":"1.0","surfaceId":"form","deleted":false,
 "components":[
 {"id":"root","component":"Card","child":"layout"},
 {"id":"layout","component":"Column","children":["row","list","divider","check","note","date","send"]},
 {"id":"row","component":"Row","children":["heading"]},
 {"id":"heading","component":"Text","text":{"path":"/heading"}},
 {"id":"list","component":"List","children":["detail"]},
 {"id":"detail","component":"Text","text":"Details"},
 {"id":"divider","component":"Divider"},
 {"id":"check","component":"CheckBox","label":"Enabled","value":{"path":"/enabled"}},
 {"id":"note","component":"TextField","label":"Note","value":{"path":"/note"}},
 {"id":"date","component":"DateTimeInput","label":"Date","value":{"path":"/date"},"enableDate":true},
 {"id":"send","component":"Button","child":"sendLabel","action":{"event":{"name":"send","context":{"enabled":{"path":"/enabled"},"note":{"path":"/note"},"date":{"path":"/date"},"fixed":7}}}},
 {"id":"sendLabel","component":"Text","text":"Send"}],
 "dataModel":{"heading":"A form","enabled":true,"note":"Original","date":"2026-10-23"}}`)
	s, err := validateSurface(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := s.plainText()
	for _, element := range []string{"A form", "Details", "---", "Enabled: true", "Note: Original", "Date: 2026-10-23", "[a] Send"} {
		if !strings.Contains(text, element) {
			t.Errorf("missing %s", element)
		}
	}
	values := map[string]json.RawMessage{"enabled": json.RawMessage(`false`), "note": json.RawMessage(`"Changed"`), "date": json.RawMessage(`"2026-10-24"`), "fixed": json.RawMessage(`7`)}
	if _, err := s.ValidateAction("send", values); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]string{"enabled": `"yes"`, "note": `false`, "date": `"2026-99-99"`, "fixed": `8`} {
		previous := values[key]
		values[key] = json.RawMessage(bad)
		if _, err := s.ValidateAction("send", values); err == nil {
			t.Errorf("accepted %s=%s", key, bad)
		}
		values[key] = previous
	}
}

func actionValueCard(t *testing.T, context map[string]any, data map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version":        "v0.9",
		"catalogId":      "lina.cards",
		"catalogVersion": "1.0",
		"surfaceId":      "limits",
		"deleted":        false,
		"components": []any{
			map[string]any{"id": "root", "component": "Column", "children": []string{"send"}},
			map[string]any{"id": "send", "component": "Button", "child": "sendLabel", "action": map[string]any{
				"event": map[string]any{"name": "send", "context": context},
			}},
			map[string]any{"id": "sendLabel", "component": "Text", "text": "Send"},
		},
		"dataModel": data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSurfaceActionValuesRespectWireLimits(t *testing.T) {
	context := map[string]any{"note": map[string]string{"path": "/note"}, "tags": map[string]string{"path": "/tags"}}
	tags := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "t"
		}
		return out
	}
	atLimit := strings.Repeat("가", 8192)
	overLimit := atLimit + "가"
	for name, tc := range map[string]struct {
		data map[string]any
		ok   bool
	}{
		"text at limit":       {map[string]any{"note": atLimit, "tags": []string{}}, true},
		"text over limit":     {map[string]any{"note": overLimit, "tags": []string{}}, false},
		"array at limit":      {map[string]any{"note": "", "tags": tags(256)}, true},
		"array over limit":    {map[string]any{"note": "", "tags": tags(257)}, false},
		"array item too long": {map[string]any{"note": "", "tags": []string{overLimit}}, false},
		"non-string in array": {map[string]any{"note": "", "tags": []any{"t", 1}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateSurface(actionValueCard(t, context, tc.data))
			if tc.ok && err != nil {
				t.Fatalf("valid action value refused: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrSurfaceValidation) {
				t.Fatalf("over-limit action value accepted: %v", err)
			}
		})
	}
}

func TestSurfaceConstantActionNumbersKeepPrecision(t *testing.T) {
	s, err := validateSurface(actionValueCard(t, map[string]any{"fixed": 9007199254740993}, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{`9007199254740993`, true},
		{`9007199254740993.0`, true},
		{`9007199254740992`, false},
		{`9007199254740994`, false},
	} {
		_, err := s.ValidateAction("send", map[string]json.RawMessage{"fixed": json.RawMessage(tc.value)})
		if tc.ok && err != nil {
			t.Fatalf("%s refused: %v", tc.value, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("accepted changed constant %s", tc.value)
		}
	}
}
