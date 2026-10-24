// SPDX-License-Identifier: Apache-2.0

package design

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode"
)

func TestCatalogRegister(t *testing.T) {
	original := catalog
	catalog = make(map[string]translation)
	t.Cleanup(func() { catalog = original })

	first := translation{"First", "첫째"}
	second := translation{"Second", "둘째"}
	register(map[string]translation{"test.first": first})
	register(map[string]translation{"test.second": second})
	for id, want := range map[string]translation{"test.first": first, "test.second": second} {
		for language, text := range map[Language]string{English: want.en, Korean: want.ko} {
			if got, ok := Lookup(language, id); !ok || got != text {
				t.Errorf("Lookup(%q, %q) = %q, %t; want %q", language, id, got, ok, text)
			}
		}
	}
}

func TestCatalogRegisterRejectsDuplicate(t *testing.T) {
	for _, sameText := range []bool{false, true} {
		t.Run(fmt.Sprint(sameText), func(t *testing.T) {
			original := catalog
			catalog = make(map[string]translation)
			t.Cleanup(func() { catalog = original })

			first := translation{"First", "첫째"}
			next := translation{"Replacement", "교체"}
			if sameText {
				next = first
			}
			const id = "test.duplicate"
			register(map[string]translation{id: first})
			defer func() {
				if value := recover(); value == nil {
					t.Error("duplicate catalog id did not panic")
				} else if !strings.Contains(fmt.Sprint(value), id) {
					t.Errorf("panic does not identify the duplicate: %v", value)
				}
				if got := catalog[id]; got != first {
					t.Errorf("duplicate replaced the existing translation: %+v", got)
				}
			}()
			register(map[string]translation{id: next})
		})
	}
}

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
				case "run_limit_reason":
					childPrefix = "run_limit.reason."
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
