// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func schemaData(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../../protocol/schema/client/1.0/envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGeneratedSchemaPreservesSource(t *testing.T) {
	data := schemaData(t)
	source, err := generate(data)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "client.gen.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			if value.Names[0].Name != "clientSchemaJSON" {
				continue
			}
			literal, ok := value.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatal("embedded schema is not a string literal")
			}
			embedded, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if embedded != string(data) {
				t.Error("embedded schema differs from the canonical source bytes")
			}
			if !strings.HasPrefix(literal.Value, "`") {
				t.Error("embedded schema is not a raw string")
			}
			return
		}
	}
	t.Fatal("generated source has no schema constant")
}

func TestSchemaChangesGenerateWorkingTypesAndValidation(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(schemaData(t), &root); err != nil {
		t.Fatal(err)
	}
	properties := root["properties"].(map[string]any)
	kind := properties["kind"].(map[string]any)
	kind["enum"] = append(kind["enum"].([]any), "probe")
	definitions := root["$defs"].(map[string]any)
	definitions["probe_code"] = map[string]any{"type": "string", "enum": []string{"ready", "stopped"}}
	definitions["probe"] = map[string]any{
		"type": "object", "properties": map[string]any{
			"code":  map[string]any{"$ref": "#/$defs/probe_code"},
			"count": map[string]any{"type": "integer", "minimum": 1},
		}, "required": []string{"code", "count"}, "additionalProperties": false,
	}
	root["allOf"] = append(root["allOf"].([]any), map[string]any{
		"if":   map[string]any{"properties": map[string]any{"kind": map[string]any{"const": "probe"}}},
		"then": map[string]any{"properties": map[string]any{"payload": map[string]any{"$ref": "#/$defs/probe"}}},
	})
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := generate(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := generate(data)
	if err != nil || !bytes.Equal(source, again) {
		t.Fatalf("generation is not deterministic: %v", err)
	}
	fixture := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(fixture, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("client.gen.go", source)
	write("go.mod", []byte("module github.com/thisisjun786/lina/kit/protocol\ngo 1.27\n"))
	for _, file := range []struct{ source, target string }{{"../../protocol.go", "protocol.go"}, {"../schema/schema.go", "internal/schema/schema.go"}} {
		data, err := os.ReadFile(file.source)
		if err != nil {
			t.Fatal(err)
		}
		write(file.target, data)
	}
	hello, err := os.ReadFile("../../../../protocol/fixtures/client/1.0/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	write("hello.json", hello)
	write("probe_test.go", []byte(`package protocol
import ("encoding/json"; "os"; "testing")
func TestProbe(t *testing.T) {
 data, err := os.ReadFile("hello.json"); if err != nil { t.Fatal(err) }
 var envelope Envelope; if err := json.Unmarshal(data, &envelope); err != nil { t.Fatal(err) }
 envelope.Kind = KindProbe
 envelope.Payload = Probe{Code: ProbeCodeReady, Count: json.Number("2")}
 data, err = json.Marshal(envelope); if err != nil { t.Fatal(err) }
 var decoded Envelope; if err := json.Unmarshal(data, &decoded); err != nil { t.Fatal(err) }
 if decoded.Payload.(Probe).Code != ProbeCodeReady { t.Fatal("schema did not generate dispatch") }
 for _, payload := range []Probe{{Code:ProbeCode("other"), Count:"2"}, {Code:ProbeCodeReady, Count:"0"}, {Code:ProbeCodeReady, Count:"1.5"}} {
  envelope.Payload = payload; if _, err := json.Marshal(envelope); err == nil { t.Fatal("schema constraint was not generated") }
 }
}
`))
	cmd := exec.Command("go", "test", "-count=1", "./...")
	cmd.Dir = fixture
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GOWORK=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated schema extension does not work: %v\n%s", err, output)
	}
}

func TestGeneratorRefusesUnsupportedOrIncompleteSchema(t *testing.T) {
	for label, change := range map[string]func(map[string]any){
		"backtick in schema": func(root map[string]any) { root["$comment"] = "Contains a ` delimiter" },
		"unknown keyword":    func(root map[string]any) { root["unevaluatedProperties"] = false },
		"open object would lose fields": func(root map[string]any) {
			root["$defs"].(map[string]any)["agent"].(map[string]any)["additionalProperties"] = true
		},
		"optional nullable would lose null": func(root map[string]any) {
			root["$defs"].(map[string]any)["agent"].(map[string]any)["required"] = []string{"agent_id", "name"}
		},
		"wrong dialect":   func(root map[string]any) { root["$schema"] = "http://json-schema.org/draft-07/schema#" },
		"missing payload": func(root map[string]any) { root["allOf"] = root["allOf"].([]any)[1:] },
		"unresolved reference": func(root map[string]any) {
			root["properties"].(map[string]any)["sender"] = map[string]any{"$ref": "#/$defs/absent"}
		},
	} {
		t.Run(label, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(schemaData(t), &root); err != nil {
				t.Fatal(err)
			}
			change(root)
			data, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := generate(data); err == nil {
				t.Fatal("silently generated an unsupported schema")
			}
		})
	}
}
