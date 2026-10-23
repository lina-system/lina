// SPDX-License-Identifier: Apache-2.0
package toolcatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestEnabledNamesKeepNativeSchemasAndLegacyReplay(t *testing.T) {
	first := []provider.ToolDecl{[]byte(`{"name":"load_tools","parameters":{"type":"object"}}`)}
	remote := provider.ToolDecl(`{"name":"remote_agent","description":"Owner-configured remote tasks","parameters":{"type":"object","properties":{"operation":{"enum":["list","send","status","cancel"]}}}}`)
	catalog := Catalog{First: first, All: []provider.ToolDecl{first[0], remote}}
	output, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": "load", "output": `{"code":"ok","output":{"loaded_tools":["remote_agent"]}}`})
	items := []provider.Item{[]byte(`{"type":"function_call","call_id":"load","name":"load_tools"}`), output}
	for _, legacy := range []bool{false, true} {
		enabled, _ := EnabledNames(first)
		if legacy {
			enabled, _ = json.Marshal(first)
		}
		request := provider.Request{Instructions: "prefix\n<enabled_tools>\n" + string(enabled) + "\n</enabled_tools>\nsuffix", Items: items, Tools: first}
		got, err := catalog.Transform(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Tools, catalog.All) || !reflect.DeepEqual(got.Items, items) {
			t.Fatal("load changed native schemas or committed round-trip items")
		}
		_, block, _ := strings.Cut(got.Instructions, "<enabled_tools>\n")
		block, _, _ = strings.Cut(block, "\n</enabled_tools>")
		if legacy {
			var schemas []provider.ToolDecl
			if err := json.Unmarshal([]byte(block), &schemas); err != nil || !reflect.DeepEqual(schemas, catalog.All) {
				t.Fatal("legacy replay instruction shape changed")
			}
		} else {
			var names []string
			if err := json.Unmarshal([]byte(block), &names); err != nil || !reflect.DeepEqual(names, []string{"load_tools", "remote_agent"}) {
				t.Fatalf("enabled names: %s (%v)", block, err)
			}
		}
		if len(request.Tools) != 1 || !strings.HasSuffix(got.Instructions, "\n</enabled_tools>\nsuffix") {
			t.Fatal("transform mutated the input or surrounding instructions")
		}
	}
}

func TestEnabledNamesHandleBuiltinsAndInvalidDeclarations(t *testing.T) {
	got, err := EnabledNames([]provider.ToolDecl{[]byte(`{"type":"web_search"}`)})
	if err != nil || string(got) != `["web_search"]` {
		t.Fatalf("built-in identity: %s %v", got, err)
	}
	if _, err := EnabledNames([]provider.ToolDecl{[]byte(`{`)}); err == nil {
		t.Fatal("invalid native schema accepted")
	}
}
