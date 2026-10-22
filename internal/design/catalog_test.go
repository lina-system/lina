// SPDX-License-Identifier: Apache-2.0

package design

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"
)

// Enumerate the actual schema, including nested enums, so adding a closed code
// requires both translations. Wording is deliberately not an assertion.
func TestCatalogCoversClosedWireCodes(t *testing.T) {
	data, err := os.ReadFile("../../protocol/schema/client/1.0/envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	var walk func(any, string)
	walk = func(value any, prefix string) {
		switch value := value.(type) {
		case map[string]any:
			if codes, ok := value["enum"].([]any); ok {
				for _, code := range codes {
					id, ok := code.(string)
					if !ok {
						continue
					}
					id = prefix + id
					for _, language := range []Language{English, Korean} {
						text, found := Lookup(language, id)
						if !found || strings.TrimSpace(text) == "" {
							t.Errorf("missing %s:%s", language, id)
						}
					}
				}
			}
			for name, child := range value {
				childPrefix := prefix
				// Running words have different meanings from connection states
				// and surface headings that share codes such as stopped/waiting.
				switch name {
				case "running_state":
					childPrefix = "running.state."
				case "running_reason":
					childPrefix = "running.reason."
				}
				walk(child, childPrefix)
			}
		case []any:
			for _, child := range value {
				walk(child, prefix)
			}
		}
	}
	walk(schema, "")
}

func TestCatalogSafetyAndFallback(t *testing.T) {
	for id, entry := range catalog {
		for _, text := range []string{entry.en, entry.ko} {
			if text == "" || strings.IndexFunc(text, unicode.IsControl) >= 0 {
				t.Errorf("invalid entry %s", id)
			}
		}
	}
	if _, found := Lookup(English, "\x1b]52;untrusted\a"); found {
		t.Fatal("unknown code accepted")
	}
	if Text(Korean, "unrecognized") != Text(Korean, "unknown") {
		t.Fatal("unsafe fallback")
	}
	if Text(Language("unsupported"), "unavailable") != Text(English, "unavailable") {
		t.Fatal("missing language fallback")
	}
	for _, locale := range []string{"ko", "ko_KR.UTF-8", "ko-KR"} {
		if LanguageFor(locale) != Korean {
			t.Errorf("locale %s", locale)
		}
	}
	for _, locale := range []string{"", "C", "en_US.UTF-8", "unknown", "korean"} {
		if LanguageFor(locale) != English {
			t.Errorf("fallback locale %s", locale)
		}
	}
}
