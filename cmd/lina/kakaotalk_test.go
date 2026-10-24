// SPDX-License-Identifier: Apache-2.0
package main

import (
	"strings"
	"testing"
)

// A typed answer reaches the add-on as typed: a password keeps its own leading
// and trailing spaces, and only the input line ending is removed. A text answer
// is trimmed, because the platform's own prompt asks for an address.
func TestReadAnswerKeepsASecretVerbatim(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret bool
		want   string
		ok     bool
	}{
		{"secret with spaces", "  pass phrase  \n", true, "  pass phrase  ", true},
		{"secret without a line ending", "pass", true, "pass", true},
		{"empty secret", "\n", true, "", false},
		{"text is trimmed", "  person@example.test \n", false, "person@example.test", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := readAnswer(strings.NewReader(c.in), false, c.secret, "prompt")
			if got != c.want || ok != c.ok {
				t.Fatalf("readAnswer(%q, secret=%v) = %q, %v; want %q, %v", c.in, c.secret, got, ok, c.want, c.ok)
			}
		})
	}
}
