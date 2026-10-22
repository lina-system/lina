// SPDX-License-Identifier: Apache-2.0
package design

import (
	"strings"
	"unicode"
)

func EnvironmentLanguage(getenv func(string) string) Language {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if locale := getenv(key); locale != "" {
			return LanguageFor(locale)
		}
	}
	return English
}

// ConversationLanguage keeps punctuation-only replies in the preceding language.
// Korean takes precedence over embedded Latin names, commands and quoted code.
func ConversationLanguage(text string, fallback Language) Language {
	if strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) {
		return Korean
	}
	if strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Latin, r) }) {
		return English
	}
	if fallback == Korean {
		return Korean
	}
	return English
}
