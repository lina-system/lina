// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const fixtures = "../../protocol/fixtures/client/1.0"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jsonValue(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestHandshakeFixtures(t *testing.T) {
	for _, name := range []string{"hello", "welcome", "unsupported_combination"} {
		t.Run(name, func(t *testing.T) {
			data := fixture(t, name)
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(jsonValue(t, data), jsonValue(t, encoded)) {
				t.Fatalf("round trip changed the envelope: %s", encoded)
			}
			switch name {
			case "hello":
				p, ok := envelope.Payload.(Hello)
				if !ok || p.Party != PartyClient || p.ID != envelope.Sender.ID || !reflect.DeepEqual(p.ProtocolVersions, []string{"1.0"}) {
					t.Fatalf("hello: %#v", envelope)
				}
			case "welcome":
				p, ok := envelope.Payload.(Welcome)
				if !ok || p.ProtocolVersion != "1.0" || p.MainID != envelope.Sender.ID || p.Epoch != envelope.Epoch || p.PersonID == "" || len(p.Agents) != 2 || p.Agents[0].Preset == nil || p.Agents[1].Preset != nil {
					t.Fatalf("welcome: %#v", envelope)
				}
			case "unsupported_combination":
				if _, ok := envelope.Payload.(ErrorPayload); !ok || envelope.Error == nil || envelope.Error.Code != ErrorUnsupportedCombination || envelope.EffectState != EffectRefused {
					t.Fatalf("refusal: %#v", envelope)
				}
			}
		})
	}
	var hello, welcome, refusal Envelope
	for name, target := range map[string]*Envelope{"hello": &hello, "welcome": &welcome, "unsupported_combination": &refusal} {
		if err := json.Unmarshal(fixture(t, name), target); err != nil {
			t.Fatal(err)
		}
	}
	if welcome.ReplyTo == nil || *welcome.ReplyTo != hello.MessageID || refusal.ReplyTo == nil || *refusal.ReplyTo != hello.MessageID {
		t.Fatal("fixtures do not answer hello")
	}
}

func TestRejectInvalidEnvelope(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unknown envelope field":   func(v map[string]any) { v["agent_id"] = "agent-example" },
		"wrong field case":         func(v map[string]any) { v["Kind"] = v["kind"]; delete(v, "kind") },
		"unknown protocol field":   func(v map[string]any) { v["protocol"].(map[string]any)["extra"] = true },
		"wrong connection":         func(v map[string]any) { v["protocol"].(map[string]any)["name"] = "node" },
		"wrong version":            func(v map[string]any) { v["protocol"].(map[string]any)["version"] = "2.0" },
		"unknown kind":             func(v map[string]any) { v["kind"] = "future_kind" },
		"unknown sender":           func(v map[string]any) { v["sender"].(map[string]any)["party"] = "worker" },
		"unknown effect":           func(v map[string]any) { v["effect_state"] = "success" },
		"refusal without error":    func(v map[string]any) { v["effect_state"] = "refused" },
		"failure without error":    func(v map[string]any) { v["effect_state"] = "failed" },
		"error kind without error": func(v map[string]any) { v["kind"] = "error"; v["payload"] = map[string]any{} },
		"unknown error":            func(v map[string]any) { v["error"] = map[string]any{"code": "oops", "detail": "example"} },
		"missing error detail":     func(v map[string]any) { v["error"] = map[string]any{"code": "malformed"} },
		"numeric epoch":            func(v map[string]any) { v["epoch"] = 1 },
		"leading zero epoch":       func(v map[string]any) { v["epoch"] = "01" },
		"negative epoch":           func(v map[string]any) { v["epoch"] = "-1" },
		"overflow epoch":           func(v map[string]any) { v["epoch"] = "18446744073709551616" },
		"invalid timestamp":        func(v map[string]any) { v["sent_at"] = "2026-02-30T00:00:00Z" },
		"non UTC timestamp":        func(v map[string]any) { v["sent_at"] = "2026-01-01T00:00:00+01:00" },
		"unknown payload field":    func(v map[string]any) { v["payload"].(map[string]any)["extra"] = true },
		"missing payload field":    func(v map[string]any) { delete(v["payload"].(map[string]any), "product_version") },
		"null capabilities":        func(v map[string]any) { v["payload"].(map[string]any)["capabilities"] = nil },
		"unversioned capability": func(v map[string]any) {
			v["payload"].(map[string]any)["capabilities"] = []any{map[string]any{"name": "example"}}
		},
		"bad offered version": func(v map[string]any) { v["payload"].(map[string]any)["protocol_versions"] = []any{"latest"} },
		"mismatched payload":  func(v map[string]any) { v["kind"] = "welcome" },
		"bad receipt": func(v map[string]any) {
			v["receipt_ref"] = map[string]any{"kind": "file", "id": "example", "revision": "1"}
		},
		"incomplete source": func(v map[string]any) { v["source_refs"] = []any{map[string]any{"id": "example"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := jsonValue(t, fixture(t, "hello")).(map[string]any)
			mutate(value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err == nil {
				t.Fatalf("accepted %s", data)
			}
		})
	}
	for field := range jsonValue(t, fixture(t, "hello")).(map[string]any) {
		t.Run("missing "+field, func(t *testing.T) {
			value := jsonValue(t, fixture(t, "hello")).(map[string]any)
			delete(value, field)
			data, _ := json.Marshal(value)
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err == nil {
				t.Fatalf("accepted missing %s", field)
			}
		})
	}
}

func TestMarshalRejectsInvalidEnvelope(t *testing.T) {
	var envelope Envelope
	if err := json.Unmarshal(fixture(t, "hello"), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Kind = KindWelcome
	if _, err := json.Marshal(envelope); err == nil {
		t.Fatal("encoded a hello payload as welcome")
	}
	if _, err := json.Marshal(Envelope{}); err == nil {
		t.Fatal("encoded a zero envelope")
	}
}

func TestReferencesAndBoundaryValues(t *testing.T) {
	var envelope Envelope
	if err := json.Unmarshal(fixture(t, "welcome"), &envelope); err != nil {
		t.Fatal(err)
	}
	ref := "example-reference"
	envelope.IdempotencyKey = &ref
	envelope.ReceiptRef = &ReceiptRef{Kind: "receipt", ID: ref, Revision: "revision-example"}
	envelope.SourceRefs = []SourceRef{{Origin: "file", Namespace: "example", ID: ref, Revision: "revision-example", Locator: "example.txt", ObservedAt: "2026-01-01T00:00:00.123456789Z", ObservedBy: &ref}}
	for _, epoch := range []string{"0", "9", "10", "18446744073709551615"} {
		envelope.Epoch = epoch
		payload := envelope.Payload.(Welcome)
		payload.Epoch = epoch
		payload.Capabilities = []Capability{{Name: "example", Version: "1.0"}}
		envelope.Payload = payload
		envelope.SentAt = "2026-01-01T00:00:00.123456789+00:00"
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Envelope
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, envelope) {
			t.Fatalf("round trip changed references or epoch: %#v", decoded)
		}
	}
}

func TestRequiredNullability(t *testing.T) {
	for field := range jsonValue(t, fixture(t, "hello")).(map[string]any) {
		t.Run(field, func(t *testing.T) {
			value := jsonValue(t, fixture(t, "hello")).(map[string]any)
			value[field] = nil
			data, _ := json.Marshal(value)
			var envelope Envelope
			err := json.Unmarshal(data, &envelope)
			nullable := slices.Contains([]string{"reply_to", "idempotency_key", "receipt_ref", "error"}, field)
			if (err == nil) != nullable {
				t.Fatalf("field %s: nullable=%v, err=%v", field, nullable, err)
			}
		})
	}
}

// Keep the public handshake API and canonical wire shape aligned as generation
// replaces the original handwritten types.
func TestSchemaAndTypesAgree(t *testing.T) {
	data, err := os.ReadFile("../../protocol/schema/client/1.0/envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Dialect              string                     `json:"$schema"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Definitions          map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Dialect != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected schema dialect %q", schema.Dialect)
	}
	objects := map[string]reflect.Type{
		"envelope": reflect.TypeFor[Envelope](), "protocol": reflect.TypeFor[Protocol](),
		"sender": reflect.TypeFor[Sender](), "receipt_ref": reflect.TypeFor[ReceiptRef](),
		"source_ref": reflect.TypeFor[SourceRef](), "error": reflect.TypeFor[Error](),
		"capability": reflect.TypeFor[Capability](), "agent": reflect.TypeFor[Agent](),
		"hello": reflect.TypeFor[Hello](), "welcome": reflect.TypeFor[Welcome](),
		"error_payload": reflect.TypeFor[ErrorPayload](),
	}
	for name, typ := range objects {
		t.Run(name, func(t *testing.T) {
			objectData := data
			if name != "envelope" {
				objectData = schema.Definitions[name]
			}
			var object struct {
				Type                 string                     `json:"type"`
				Properties           map[string]json.RawMessage `json:"properties"`
				Required             []string                   `json:"required"`
				AdditionalProperties *bool                      `json:"additionalProperties"`
			}
			if err := json.Unmarshal(objectData, &object); err != nil {
				t.Fatal(err)
			}
			if object.Type != "object" || object.AdditionalProperties == nil || *object.AdditionalProperties {
				t.Fatal("schema must declare a closed object")
			}
			if len(object.Properties) != typ.NumField() {
				t.Fatal("schema and Go field counts differ")
			}
			requiredCount := 0
			for i := 0; i < typ.NumField(); i++ {
				tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")
				field := tag[0]
				if _, ok := object.Properties[field]; !ok {
					t.Fatalf("schema missing property %s", field)
				}
				optional := slices.Contains(tag[1:], "omitempty")
				if slices.Contains(object.Required, field) == optional {
					t.Fatalf("schema required membership differs from json tag for %s", field)
				}
				if !optional {
					requiredCount++
				}
			}
			if len(object.Required) != requiredCount {
				t.Fatal("schema required count differs from non-omitempty fields")
			}
		})
	}
	for name, values := range map[string][]string{
		"error_code":   {string(ErrorUnsupportedCombination), string(ErrorCapabilityNotNegotiated), string(ErrorMalformed), string(ErrorStaleEpoch), string(ErrorStaleRevision), string(ErrorNotAuthorized), string(ErrorOutOfScope), string(ErrorDuplicate), string(ErrorUnavailable)},
		"effect_state": {string(EffectNone), string(EffectRefused), string(EffectFailed), string(EffectApplied), string(EffectCancelled), string(EffectUnknown)},
		"party":        {string(PartyCore), string(PartyClient), string(PartyNode)},
	} {
		var definition struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(schema.Definitions[name], &definition); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(values, definition.Enum) {
			t.Fatalf("%s values disagree with schema", name)
		}
		for _, value := range values {
			input := jsonValue(t, fixture(t, "unsupported_combination")).(map[string]any)
			switch name {
			case "error_code":
				input["error"].(map[string]any)["code"] = value
			case "effect_state":
				input["effect_state"] = value
			case "party":
				input["sender"].(map[string]any)["party"] = value
			}
			data, _ := json.Marshal(input)
			var envelope Envelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("rejected %s %s: %v", name, value, err)
			}
		}
	}
	for _, value := range []string{"0", "1", "10", "18446744073709551615", "18446744073709551616", "99999999999999999999", "01", "-1", "1.0", "", "0\n"} {
		number, err := strconv.ParseUint(value, 10, 64)
		valid := err == nil && strconv.FormatUint(number, 10) == value
		input := jsonValue(t, fixture(t, "hello")).(map[string]any)
		input["epoch"] = value
		data, _ := json.Marshal(input)
		var envelope Envelope
		if (json.Unmarshal(data, &envelope) == nil) != valid {
			t.Fatalf("incorrect uint64 epoch validation for %q", value)
		}
	}
}
