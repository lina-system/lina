// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func clientMessage(t *testing.T, kind string, payload any) map[string]any {
	t.Helper()
	value := jsonValue(t, fixture(t, "hello")).(map[string]any)
	value["kind"], value["payload"] = kind, payload
	return value
}

func roundTrip(t *testing.T, value map[string]any) Envelope {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value, jsonValue(t, encoded)) {
		t.Fatalf("round trip changed JSON: %s", encoded)
	}
	return envelope
}

func concretePayloads() map[string]any {
	return map[string]any{
		"stop_result":        map[string]any{"run_id": "run-1", "outcome": "cancelled"},
		"tool_activity":      map[string]any{"call_id": "call-example", "tool_name": "Reading file", "status": "started"},
		"restart_status":     map[string]any{"owed": false},
		"record_request":     map[string]any{"operation": "query", "view": "memos"},
		"record_result":      map[string]any{"operation": "query", "rows": []any{}},
		"a2a_request":        map[string]any{"operation": "list"},
		"local_task":         map[string]any{"task": "intent", "text": "open safari"},
		"local_task_result":  map[string]any{"route": "local", "reason": "fit", "provider": "apple", "model": "apple-foundation-on-device", "served_id": "0123456789abcdef0123456789abcdef"},
		"a2a_result":         map[string]any{"operation": "list", "connections": []any{}},
		"web_request":        map[string]any{"operation": "status"},
		"web_result":         map[string]any{"provider": "firecrawl", "configured": false},
		"export_result":      map[string]any{"directory": "staging/export/synthetic", "manifest": "{}"},
		"conversation_start": map[string]any{"agent_id": "agent-example"},
		"approval_request":   map[string]any{"choices": []any{"once", "deny", "always_allow"}},
		"approval_answer":    map[string]any{"choice": "once"},
		"question": map[string]any{"conversation_id": "conversation-example", "form": map[string]any{
			"plugin_name": "example-plugin", "fields": []any{
				map[string]any{"name": "note", "type": "text"},
				map[string]any{"name": "count", "type": "number"},
				map[string]any{"name": "enabled", "type": "boolean"},
				map[string]any{"name": "mode", "type": "choice", "choices": []any{"a", "b"}},
			},
		}},
		"question_answer": map[string]any{"answer": "accept", "values": map[string]any{"note": "example", "count": float64(2.5), "enabled": true, "mode": "a"}},
		"result": map[string]any{
			"result_id": "result-example", "revision": float64(1), "type": "summary", "title": "Example", "summary": "Example summary",
			"status": "preview", "saved_as": nil, "revised_by": "agent-example",
			"provenance": map[string]any{"agent_id": "agent-example", "conversation_id": "conversation-example", "turn_id": "turn-example", "task_id": nil},
		},
		"follow_up":          map[string]any{"run_id": "run-example", "text": "Read appendix"},
		"multi_task_control": map[string]any{"action": "stop_all"},
		"progress_digest":    map[string]any{"lines": []any{}, "total": float64(0), "more": float64(0), "link": "lina://progress"},
		"result_get":         map[string]any{"result_id": "result-example"},
		"result_edit":        map[string]any{"result_id": "result-example", "base_revision": float64(1), "body": map[string]any{}},
		"result_save":        map[string]any{"result_id": "result-example", "revision": float64(1), "destination": map[string]any{}},
		"readiness_failure":  map[string]any{"step": "preparation-example"},
		"account_request":    map[string]any{"operation": "sign_in", "provider": "chatgpt", "purpose": "login"},
		"account_result":     map[string]any{"operation": "sign_in", "account_id": "account-example", "status": "ok"},
		"sign_in_listening":  map[string]any{"port": float64(32123)},
		"sign_in_open":       map[string]any{"url": "https://auth.openai.com/api/accounts/authorize?state=synthetic"},
		"sign_in_redirect":   map[string]any{"code": "synthetic", "state": "synthetic"},
		"snapshot": map[string]any{
			"topic": "conversation:example", "cursor": map[string]any{"epoch": "7", "seq": float64(2)},
			"revocation_epoch": "0",
			"items": []any{map[string]any{
				"kind": "item_done", "key": "item-example", "revision": float64(2),
				"payload": map[string]any{"text": "complete"},
			}},
		},
		"work_switch_request": map[string]any{"operation": "switch_status", "switch_id": "switch-1"},
		"work_switch_result":  map[string]any{"switch_id": "switch-1", "state": "approved", "approval": "input-1", "snapshot": "hash", "tasks": []any{"task-1"}, "events": []any{}},
		"resync":              map[string]any{"topic": "conversation:example", "cursor": map[string]any{"epoch": "7", "seq": float64(2)}},
		"resync_required":     map[string]any{"topic": "conversation:example", "cursor": map[string]any{"epoch": "7", "seq": float64(2)}},
		"plan_request":        map[string]any{"operation": "get", "agent_id": "agent", "result_id": "plan"},
	}
}

func TestEveryClientKindRoundTripsWithTypedPayload(t *testing.T) {
	concrete := concretePayloads()
	types := map[reflect.Type]string{}
	// Every schema kind must have a distinct generated payload, even while reserved.
	for _, kind := range clientSchema.Properties["kind"].Enum {
		t.Run(kind, func(t *testing.T) {
			payload, ok := concrete[kind]
			if !ok {
				payload = map[string]any{}
			}
			value := clientMessage(t, kind, payload)
			switch kind {
			case "hello", "welcome":
				value = jsonValue(t, fixture(t, kind)).(map[string]any)
			case "error":
				value = jsonValue(t, fixture(t, "unsupported_combination")).(map[string]any)
			case "result_save":
				value["idempotency_key"] = "example-save-key"
			}
			envelope := roundTrip(t, value)
			typ := reflect.TypeOf(envelope.Payload)
			if typ == nil || typ.Kind() != reflect.Struct {
				t.Fatalf("kind %s has no concrete generated payload: %T", kind, envelope.Payload)
			}
			if earlier, found := types[typ]; found {
				t.Fatalf("%s and %s share a payload type", kind, earlier)
			}
			types[typ] = kind
			value["payload"].(map[string]any)["unspecified"] = true
			data, _ := json.Marshal(value)
			if err := json.Unmarshal(data, &envelope); err == nil {
				t.Fatal("accepted an unspecified payload field")
			}
		})
	}
}

func TestNumbersRetainTheirExactJSONValue(t *testing.T) {
	for _, number := range []string{"9007199254740993.125", "1e400", "-0", "2.5000"} {
		value := clientMessage(t, "question_answer", map[string]any{
			"answer": "accept", "values": map[string]any{"amount": json.Number(number)},
		})
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var envelope Envelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		answer := envelope.Payload.(QuestionAnswer)
		if got := (*answer.Values)["amount"].Number; got == nil || string(*got) != number {
			t.Fatalf("number changed: %v", got)
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			t.Fatal(err)
		}
		if got := (*envelope.Payload.(QuestionAnswer).Values)["amount"].Number; got == nil || string(*got) != number {
			t.Fatal("encoding rounded the answer")
		}
	}
}

func TestOptionalResultFieldsAndSaveKey(t *testing.T) {
	payload := concretePayloads()["result_get"].(map[string]any)
	payload["revision"] = float64(2)
	roundTrip(t, clientMessage(t, "result_get", payload))
	payload = concretePayloads()["result"].(map[string]any)
	payload["status"], payload["saved_as"] = "saved", map[string]any{}
	roundTrip(t, clientMessage(t, "result", payload))
	value := clientMessage(t, "result_save", concretePayloads()["result_save"])
	data, _ := json.Marshal(value)
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err == nil {
		t.Fatal("accepted result_save without an idempotency key")
	}
}

func TestQuestionAndResultConstraints(t *testing.T) {
	cases := map[string]struct {
		kind   string
		mutate func(map[string]any)
	}{
		"nested form": {"question", func(p map[string]any) {
			p["form"].(map[string]any)["fields"] = []any{map[string]any{"name": "nested", "type": "object"}}
		}},
		"choice without choices": {"question", func(p map[string]any) {
			delete(p["form"].(map[string]any)["fields"].([]any)[3].(map[string]any), "choices")
		}},
		"nonchoice options": {"question", func(p map[string]any) {
			p["form"].(map[string]any)["fields"].([]any)[0].(map[string]any)["choices"] = []any{"a"}
		}},
		"nested answer":           {"question_answer", func(p map[string]any) { p["values"].(map[string]any)["nested"] = map[string]any{"a": true} }},
		"accept without values":   {"question_answer", func(p map[string]any) { delete(p, "values") }},
		"decline with values":     {"question_answer", func(p map[string]any) { p["answer"] = "decline" }},
		"unknown answer":          {"question_answer", func(p map[string]any) { p["answer"] = "approve" }},
		"future result type":      {"result", func(p map[string]any) { p["type"] = "app" }},
		"zero revision":           {"result", func(p map[string]any) { p["revision"] = float64(0) }},
		"fractional revision":     {"result", func(p map[string]any) { p["revision"] = 1.5 }},
		"saved without reference": {"result", func(p map[string]any) { p["status"] = "saved" }},
		"preview with reference":  {"result", func(p map[string]any) { p["saved_as"] = map[string]any{} }},
		"body without text":       {"result", func(p map[string]any) { p["body"] = map[string]any{} }},
		"unreserved result body":  {"result_edit", func(p map[string]any) { p["body"] = map[string]any{"html": "example"} }},
		"zero edit revision":      {"result_edit", func(p map[string]any) { p["base_revision"] = float64(0) }},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			payload := concretePayloads()[tc.kind].(map[string]any)
			tc.mutate(payload)
			data, _ := json.Marshal(clientMessage(t, tc.kind, payload))
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err == nil {
				t.Fatalf("accepted invalid %s: %s", tc.kind, data)
			}
		})
	}
	for _, answer := range []string{"decline", "cancel"} {
		roundTrip(t, clientMessage(t, "question_answer", map[string]any{"answer": answer}))
	}
	payload := concretePayloads()["result"].(map[string]any)
	payload["text"], payload["body"] = "Example", map[string]any{}
	roundTrip(t, clientMessage(t, "result", payload))
}
