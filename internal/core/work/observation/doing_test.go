// SPDX-License-Identifier: Apache-2.0
package observation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanUntrustedDisplayRecords(t *testing.T) {
	for _, raw := range []string{
		"\x1b[31mLogin\x1b[0m",
		"\x1b]8;;https://example.invalid\aLogin\x1b]8;;\a",
		"\u009b31mLogin\u009b0m",
		"\u009d8;;hidden\u009cLogin",
		"Login\x1b]52;c;hidden",
		"Login\u202e",
	} {
		if got := Clean(raw); got != "Login" {
			t.Fatalf("controls survived: %q", got)
		}
	}
	raw := "Login token=syntheticcredential " + strings.Repeat("한", 130)
	got := Clean(raw)
	if strings.Contains(got, "syntheticcredential") || utf8.RuneCountInString(got) > 120 || Clean(got) != got {
		t.Fatalf("unsafe or non-idempotent: %q", got)
	}
}
