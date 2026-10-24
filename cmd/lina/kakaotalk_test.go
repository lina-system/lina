// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"strings"
	"testing"
)

// A typed answer reaches the add-on as typed: a password keeps its own leading
// and trailing spaces, and only the input line ending is removed. A text answer
// is trimmed, because the platform's own prompt asks for an address.
func TestAnswersKeepASecretVerbatim(t *testing.T) {
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
			prompts := newAnswers(nil, false)
			prompts.reader = bufio.NewReader(strings.NewReader(c.in))
			got, ok := prompts.read(c.secret, "prompt")
			if got != c.want || ok != c.ok {
				t.Fatalf("read(%q, secret=%v) = %q, %v; want %q, %v", c.in, c.secret, got, ok, c.want, c.ok)
			}
		})
	}
}

// One reader spans every step: input piped for the email, the password and the
// phone confirmation is not lost when the first prompt returns.
func TestAnswersKeepPipedInputAcrossPrompts(t *testing.T) {
	prompts := newAnswers(nil, false)
	prompts.reader = bufio.NewReader(strings.NewReader("person@example.test\n  secret  \n\n"))
	if got, ok := prompts.read(false, "KakaoTalk email"); !ok || got != "person@example.test" {
		t.Fatalf("email %q, %v", got, ok)
	}
	if got, ok := prompts.read(true, "KakaoTalk password"); !ok || got != "  secret  " {
		t.Fatalf("password %q, %v", got, ok)
	}
	if !prompts.wait("Confirm this code on your phone: 4821 (then press Enter)") {
		t.Fatal("the confirmation line was lost")
	}
}
