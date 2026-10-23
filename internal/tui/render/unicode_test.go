// SPDX-License-Identifier: Apache-2.0

package render

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestUnicodeGraphemeConformance(t *testing.T) {
	data, err := os.ReadFile("testdata/GraphemeBreakTest.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "88ead7e1c192b5d9767eb4c54cb62e177ab82afd972efc247e04599fab66b719" {
		t.Fatal("Unicode conformance corpus changed")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	number, cases := 0, 0
	for scanner.Scan() {
		number++
		text, _, _ := strings.Cut(scanner.Text(), "#")
		var expected []string
		var cluster strings.Builder
		for _, field := range strings.Fields(text) {
			if field == "÷" {
				if cluster.Len() > 0 {
					expected = append(expected, cluster.String())
					cluster.Reset()
				}
				continue
			}
			if field == "×" {
				continue
			}
			r, err := strconv.ParseInt(field, 16, 32)
			if err != nil {
				t.Fatal(err)
			}
			cluster.WriteRune(rune(r))
		}
		if len(expected) == 0 {
			continue
		}
		cases++
		var got []string
		for remaining := strings.Join(expected, ""); remaining != ""; {
			first, rest := nextCluster(remaining)
			got = append(got, first)
			remaining = rest
		}
		if !slices.Equal(got, expected) {
			t.Errorf("Unicode line %d: %q != %q", number, got, expected)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if cases < 700 {
		t.Fatal("missing conformance cases")
	}
}

func TestCursorAndClipping(t *testing.T) {
	for _, mode := range []WidthMode{WCWidth, Graphemes} {
		m := Unicode{mode}
		for _, text := range []string{"한글", "a한é", "👩‍💻", "한"} {
			if got := Cursor(text, len(text), 80, m); got.Row != 0 || got.Column != m.Width(text) {
				t.Fatalf("cursor %q: %+v", text, got)
			}
			cluster, _ := nextCluster(text)
			if len(cluster) > 1 {
				if got := Cursor(text, 1, 80, m); got.Column != 0 {
					t.Fatal("cursor inside cluster")
				}
			}
			for width := 1; width < 10; width++ {
				cut := Truncate(text, width, m)
				if m.Width(cut) > width {
					t.Fatal("clipping overflow")
				}
				rest := text
				for rest != "" && len(text)-len(rest) < len(cut) {
					_, rest = nextCluster(rest)
				}
				if len(text)-len(rest) != len(cut) {
					t.Fatal("clipping split cluster")
				}
			}
		}
		if got := Cursor("한글a", len("한글a"), 4, m); got.Row != 1 || got.Column != 1 {
			t.Fatalf("wrapped cursor: %+v", got)
		}
	}
}

func FuzzChunkedStream(f *testing.F) {
	for _, s := range []string{"한글 👩‍💻 é", "🇰🇷🇺🇸", "\x1b]52;c;hidden\aend", "a \n b", "\u0600\u0308a", "♥️"} {
		f.Add(s, uint8(7), uint8(2))
	}
	f.Fuzz(func(t *testing.T, input string, w, chunk uint8) {
		if len(input) > 4096 {
			t.Skip("input over 4096 bytes")
		}
		width, step := 1+int(w%80), 1+int(chunk%31)
		config := Config{Width: width, Mode: Graphemes}
		s := NewStream("id", config, nil)
		rev := uint64(0)
		for i := 0; i < len(input); i += step {
			rev++
			s.Append(rev, input[i:min(len(input), i+step)])
		}
		// Flush incomplete UTF-8 as a complete message would, without sealing the
		// stream; the extra safe rune also completes its final grapheme boundary.
		rev++
		s.Append(rev, " ")
		want := NewStream("id", config, nil)
		want.Seal(1, input+" ")
		if s.Len() != want.Len() {
			t.Fatalf("row count %d != %d", s.Len(), want.Len())
		}
		for i := 0; i < s.Len(); i++ {
			if s.At(i).Text != want.At(i).Text {
				t.Fatalf("row %d: %q != %q", i, s.At(i).Text, want.At(i).Text)
			}
		}
	})
}
