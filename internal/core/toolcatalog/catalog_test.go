// SPDX-License-Identifier: Apache-2.0
package toolcatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestName(t *testing.T) {
	if got := Name([]byte(`{"name":"web_search","parameters":{"type":"object"}}`)); got != "web_search" {
		t.Fatalf("valid declaration name = %q", got)
	}
	if got := Name([]byte(`{"name":`)); got != "" {
		t.Fatalf("malformed JSON name = %q", got)
	}
}

func TestTransform(t *testing.T) {
	loadTools := provider.ToolDecl(`{"name":"load_tools"}`)
	web := provider.ToolDecl(`{"name":"web_search","parameters":{"type":"object"}}`)
	calc := provider.ToolDecl(`{"name":"calculate"}`)
	firstWeb := provider.ToolDecl(`{"name":"web_search","parameters":{"from":"first"}}`)

	t.Run("nil catalog", func(t *testing.T) {
		in := provider.Request{
			Instructions: "keep",
			Items:        []provider.Item{[]byte(`{"type":"function_call_output","call_id":"c","output":"{}"}`)},
			Tools:        []provider.ToolDecl{loadTools},
		}
		var catalog *Catalog
		got, err := catalog.Transform(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, in) {
			t.Fatalf("request changed: %#v", got)
		}
	})

	t.Run("truncated history", func(t *testing.T) {
		items := []provider.Item{[]byte(`{}`)}
		for _, offset := range []int{-1, len(items) + 1} {
			_, err := (&Catalog{Offset: offset}).Transform(t.Context(), provider.Request{Items: items})
			if err == nil || err.Error() != "tool_catalog_history_truncated" {
				t.Fatalf("offset %d: %v", offset, err)
			}
		}
	})

	t.Run("ok load appends after first and rewrites enabled tools", func(t *testing.T) {
		catalog := &Catalog{
			First:  []provider.ToolDecl{loadTools},
			All:    []provider.ToolDecl{web, calc},
			Offset: 0,
		}
		in := provider.Request{
			Instructions: "prefix\n<enabled_tools>\n[]\n</enabled_tools>\nsuffix",
			Items: []provider.Item{
				callItem(t, "load", "load_tools"),
				outputItem(t, "load", loop.OK, "calculate", "web_search"),
			},
		}
		got := transform(t, catalog, in)
		wantTools := []provider.ToolDecl{loadTools, web, calc}
		if !reflect.DeepEqual(got.Tools, wantTools) {
			t.Fatalf("tools = %s, want %s", got.Tools, wantTools)
		}
		wantInstructions := "prefix\n<enabled_tools>\n[\"load_tools\",\"web_search\",\"calculate\"]\n</enabled_tools>\nsuffix"
		if got.Instructions != wantInstructions {
			t.Fatalf("instructions = %q", got.Instructions)
		}
	})

	t.Run("ignores history before offset unmatched calls and non-ok results", func(t *testing.T) {
		prior := []provider.Item{
			callItem(t, "old", "load_tools"),
			outputItem(t, "old", loop.OK, "calculate"),
		}
		catalog := &Catalog{
			First:  []provider.ToolDecl{loadTools},
			All:    []provider.ToolDecl{web, calc},
			Offset: len(prior),
		}
		in := provider.Request{
			Instructions: "prefix\n<enabled_tools>\n[]\n</enabled_tools>\nsuffix",
			Tools:        []provider.ToolDecl{provider.ToolDecl(`{"name":"stale"}`)},
			Items: append(prior,
				outputItem(t, "early", loop.OK, "web_search"),
				callItem(t, "early", "load_tools"),
				callItem(t, "other", "web_search"),
				outputItem(t, "other", loop.OK, "calculate"),
				callItem(t, "denied", "load_tools"),
				outputItem(t, "denied", loop.Denied, "web_search", "calculate"),
			),
		}
		got := transform(t, catalog, in)
		if !reflect.DeepEqual(got.Tools, catalog.First) {
			t.Fatalf("tools = %s", got.Tools)
		}
		wantInstructions := "prefix\n<enabled_tools>\n[\"load_tools\"]\n</enabled_tools>\nsuffix"
		if got.Instructions != wantInstructions {
			t.Fatalf("instructions = %q", got.Instructions)
		}
	})

	t.Run("does not duplicate tools already in first", func(t *testing.T) {
		catalog := &Catalog{
			First:  []provider.ToolDecl{loadTools, firstWeb},
			All:    []provider.ToolDecl{web, calc},
			Offset: 0,
		}
		in := provider.Request{
			Instructions: "prefix\n<enabled_tools>\n[]\n</enabled_tools>\nsuffix",
			Items: []provider.Item{
				callItem(t, "load", "load_tools"),
				outputItem(t, "load", loop.OK, "web_search", "calculate"),
			},
		}
		got := transform(t, catalog, in)
		wantTools := []provider.ToolDecl{loadTools, firstWeb, calc}
		if !reflect.DeepEqual(got.Tools, wantTools) {
			t.Fatalf("tools = %s, want %s", got.Tools, wantTools)
		}
		if got.Instructions != "prefix\n<enabled_tools>\n[\"load_tools\",\"web_search\",\"calculate\"]\n</enabled_tools>\nsuffix" {
			t.Fatalf("instructions = %q", got.Instructions)
		}
	})

	t.Run("leaves instructions without the block untouched", func(t *testing.T) {
		catalog := &Catalog{
			First:  []provider.ToolDecl{loadTools},
			All:    []provider.ToolDecl{web},
			Offset: 0,
		}
		in := provider.Request{
			Instructions: "no enabled tools block",
			Items: []provider.Item{
				callItem(t, "load", "load_tools"),
				outputItem(t, "load", loop.OK, "web_search"),
			},
		}
		got := transform(t, catalog, in)
		if got.Instructions != in.Instructions {
			t.Fatalf("instructions = %q", got.Instructions)
		}
		if !reflect.DeepEqual(got.Tools, []provider.ToolDecl{loadTools, web}) {
			t.Fatalf("tools = %s", got.Tools)
		}
	})
}

func transform(t *testing.T, catalog *Catalog, request provider.Request) provider.Request {
	t.Helper()
	got, err := catalog.Transform(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func callItem(t *testing.T, id, name string) provider.Item {
	t.Helper()
	return mustItem(t, map[string]string{"type": "function_call", "name": name, "call_id": id})
}

func outputItem(t *testing.T, id string, code loop.Code, loaded ...string) provider.Item {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"loaded_tools": loaded})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(loop.Result{ID: id, Code: code, Output: payload})
	if err != nil {
		t.Fatal(err)
	}
	return mustItem(t, map[string]string{
		"type":    "function_call_output",
		"call_id": id,
		"output":  string(result),
	})
}

func mustItem(t *testing.T, value any) provider.Item {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

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
