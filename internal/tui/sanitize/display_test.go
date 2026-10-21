// SPDX-License-Identifier: Apache-2.0

package sanitize

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestDisplay(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"한글 e\u0301 👩‍💻\nnext", "한글 e\u0301 👩‍💻\nnext"},
		{"a\x1b[31mb\x1b[0mc", "abc"},
		{"a\x1b]52;c;secret\aZ", "aZ"},
		{"a\x1b]8;;https://example.invalid\x1b\\link\x1b]8;;\x1b\\", "alink"},
		{"a\u009d52;c;secret\u009cZ", "aZ"},
		{"a\x1bPsecret\x1b\\Z", "aZ"},
		{"a\x1b]unterminated", "a"},
		{"a\u202eb\u2066c\u2069\u200e\u061c", "abc"},
		{"a\r\nb\t\x00\x7f\u0085", "a\nb    "},
		{"a\xffb", "a�b"},
	} {
		if got := Display(tc.in); got != tc.want {
			t.Errorf("Display(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func FuzzDisplay(f *testing.F) {
	for _, seed := range []string{"한글 👩‍💻", "\x1b]52;c;owned\a", "\u202e\u2066", "\x1b[31mx", "\x1bPabc\x1b\\", "\xff\x00\r\n", "\u009d8;;url\u009c"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Display(s)
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8")
		}
		for _, r := range got {
			if (unicode.IsControl(r) && r != '\n') || strings.ContainsRune("\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069", r) {
				t.Fatalf("unsafe rune %U", r)
			}
		}
		if Display(got) != got {
			t.Fatal("not idempotent")
		}
	})
}

func TestStreamAtEveryByteBoundary(t *testing.T) {
	for _, input := range []string{
		"한글 👩‍💻 é\nnext", "a\x1b]52;c;payload\x1b\\Z",
		"a\u009d52;c;payload\u009cZ", "a\x1b[31mred\x1b[0mZ",
		"a\x1bPprivate\x1b\\Z", "a\xffb\u202ec",
	} {
		for split := 0; split <= len(input); split++ {
			s, first := (Stream{}).Feed(input[:split])
			_, last := s.Feed(input[split:])
			if first+last != Display(input) {
				t.Fatalf("split %d of %q: %q", split, input, first+last)
			}
		}
	}
}
