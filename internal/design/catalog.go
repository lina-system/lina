// SPDX-License-Identifier: Apache-2.0

package design

import "strings"

type Language string

const (
	English Language = "en"
	Korean  Language = "ko"
)

func LanguageFor(locale string) Language {
	locale = strings.ToLower(locale)
	if locale == "ko" || strings.HasPrefix(locale, "ko_") || strings.HasPrefix(locale, "ko-") {
		return Korean
	}
	return English
}

// Lookup accepts only catalog ids; it never turns unknown peer text into UI.
func Lookup(language Language, id string) (string, bool) {
	entry, ok := catalog[id]
	if !ok {
		return "", false
	}
	if language == Korean {
		return entry.ko, true
	}
	return entry.en, true
}

func Text(language Language, id string) string {
	if text, ok := Lookup(language, id); ok {
		return text
	}
	text, _ := Lookup(language, "unknown")
	return text
}

type translation struct{ en, ko string }

// Keys are wire codes or local presentation ids, never English source text.
// Keep both languages together so a new entry cannot omit a translation.
var catalog = make(map[string]translation)

// register is called only during package initialization by each surface catalog.
// A duplicate is a programming error, even if both translations are identical.
func register(entries map[string]translation) {
	for id, entry := range entries {
		if _, exists := catalog[id]; exists {
			panic("design: duplicate catalog id: " + id)
		}
		catalog[id] = entry
	}
}
