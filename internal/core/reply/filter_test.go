// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/reply"
)

func lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func config() reply.Config {
	return reply.Config{Budget: reply.ChatCeiling(), Language: "ko", RenderLines: lines}
}

func filter(t *testing.T, c reply.Config) *reply.Filter {
	t.Helper()
	f, err := reply.NewFilter(c)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCounting(t *testing.T) {
	for _, tc := range []struct {
		name, text, language string
		ratios               map[string]float64
		chars, want          int
	}{
		{"NFC Hangul", "\u1100\u1161 \u1102\u1161", "ko-KR", nil, 2, 2},
		{"NFC accent", "e\u0301", "fr", map[string]float64{"fr": 1}, 1, 1},
		{"Korean whitespace", "가\t 나\n다\u00a0라", "ko", nil, 4, 4},
		{"Unicode scalars", "👩‍💻", "ko", nil, 3, 3},
		{"English spaces", "a b", "en-US", map[string]float64{"en": 2}, 3, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config()
			c.Language = tc.language
			c.Ratios = tc.ratios
			c.Budget.Chars = tc.chars
			f := filter(t, c)
			v, err := f.Push(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if v.Count.Chars != tc.chars || v.Cut || f.Budget().Chars != tc.want {
				t.Fatalf("view=%+v budget=%+v", v, f.Budget())
			}
			if v.Text != tc.text {
				t.Fatal("counting normalized the displayed text")
			}
		})
	}
}

func TestCeilingsAndCards(t *testing.T) {
	for _, tc := range []struct {
		name, text, visible string
		cut                 bool
	}{
		{"at chars", "끝. " + strings.Repeat("가", 298), "끝. " + strings.Repeat("가", 298), false},
		{"over chars", "끝. " + strings.Repeat("가", 299), "끝.", true},
		{"at lines", "one.\ntwo.\nthree.\nfour.\nfive.", "one.\ntwo.\nthree.\nfour.\nfive.", false},
		{"over lines", "one.\ntwo.\nthree.\nfour.\nfive.\nsix.", "one.\ntwo.\nthree.\nfour.\nfive.", true},
		{"no complete sentence", strings.Repeat("가", 301), "", true},
		{"sentence past ceiling", strings.Repeat("가", 300) + ".", "", true},
		{"decimal", "값은 3.14이고 " + strings.Repeat("가", 301), "", true},
		{"quoted sentence", "“끝!” " + strings.Repeat("가", 301), "“끝!”", true},
		{"CJK sentence", "끝。" + strings.Repeat("가", 301), "끝。", true},
		{"Devanagari danda", "यह पूरा है। " + strings.Repeat("क", 301), "यह पूरा है।", true},
		{"Devanagari double danda", "यह पूरा है॥ " + strings.Repeat("क", 301), "यह पूरा है॥", true},
		{"Urdu full stop", "یہ مکمل ہے۔ " + strings.Repeat("ک", 301), "یہ مکمل ہے۔", true},
		{"quoted Urdu question", "“کیا یہ مکمل ہے؟” " + strings.Repeat("ک", 301), "“کیا یہ مکمل ہے؟”", true},
		{"Armenian full stop", "Ավարտված է։ " + strings.Repeat("ա", 301), "Ավարտված է։", true},
		{"Myanmar full stop", "ပြီးပြီ။ " + strings.Repeat("က", 301), "ပြီးပြီ။", true},
		{"Ethiopic full stop", "ተጠናቋል። " + strings.Repeat("ሀ", 301), "ተጠናቋል።", true},
		{"unterminated paragraph", "unfinished\n" + strings.Repeat("가", 301), "", true},
		{"Unicode sentence past ceiling", strings.Repeat("क", 300) + "।", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := filter(t, config())
			v, err := f.Push(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if v.Text != tc.visible || v.Cut != tc.cut {
				t.Fatalf("view=%+v", v)
			}
			if f.FullText() != tc.text {
				t.Fatal("full reply changed")
			}
			if !tc.cut {
				if v.Card != nil {
					t.Fatal("unexpected card")
				}
				return
			}
			if v.Card == nil || v.Card.Type != "document_draft" || v.Card.Status != "preview" || v.Card.Text != tc.text || len(v.Card.Body.Blocks) != 1 || v.Card.Body.Blocks[0].Text != tc.text || v.Card.Body.FromSummary != nil {
				t.Fatalf("card=%+v", v.Card)
			}
		})
	}
}

func TestStreamingCutAndRetention(t *testing.T) {
	f := filter(t, config())
	first := "끝. " + strings.Repeat("가", 290)
	v, err := f.Push(first)
	if err != nil || v.Text != first || v.Cut {
		t.Fatalf("first words delayed: %+v %v", v, err)
	}
	v, err = f.Push(strings.Repeat("나", 20))
	if err != nil || !v.Cut || v.Text != "끝." {
		t.Fatalf("cut=%+v %v", v, err)
	}
	v, err = f.Push(" 그리고 계속되는 원문.")
	if err != nil || v.Text != "끝." || !strings.HasSuffix(v.Card.Text, " 그리고 계속되는 원문.") || v.Card.Text != f.FullText() {
		t.Fatalf("tail=%+v %v", v, err)
	}
}

func TestRenderedLines(t *testing.T) {
	c := config()
	// Synthetic surface wraps every four scalar values; physical line breaks
	// alone cannot establish the rendered-line budget.
	c.RenderLines = func(s string) int { return (utf8.RuneCountInString(s) + 3) / 4 }
	f := filter(t, c)
	v, err := f.Push("OK! " + strings.Repeat("x", 17))
	if err != nil || !v.Cut || v.Text != "OK!" || v.Count.Lines != 6 {
		t.Fatalf("view=%+v %v", v, err)
	}
}

func TestChunkBoundaries(t *testing.T) {
	for _, s := range []string{
		"끝. " + strings.Repeat("\u1100\u1161 ", 301),
		"첫 문장! 다음 문장? " + strings.Repeat("가", 301),
		"첫째.\n둘째.\n셋째.\n넷째.\n다섯째.\n여섯째.",
		"यह पूरा है। " + strings.Repeat("क", 301),
		"“یہ مکمل ہے۔” " + strings.Repeat("ک", 301),
	} {
		whole := filter(t, config())
		want, err := whole.Push(s)
		if err != nil {
			t.Fatal(err)
		}
		for at := range s {
			f := filter(t, config())
			if _, err := f.Push(s[:at]); err != nil {
				t.Fatal(err)
			}
			got, err := f.Push(s[at:])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || f.FullText() != s {
				t.Fatalf("chunk at %d differs", at)
			}
		}
		f := filter(t, config())
		var got reply.View
		for _, r := range s {
			got, err = f.Push(string(r))
			if err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("scalar chunks differ")
		}
	}
}

func TestInvalidConfigurationAndInput(t *testing.T) {
	for _, edit := range []func(*reply.Config){
		func(c *reply.Config) { c.Budget.Lines = 0 },
		func(c *reply.Config) { c.Budget.Chars = -1 },
		func(c *reply.Config) { c.RenderLines = nil },
		func(c *reply.Config) { c.Language = "en" },
		func(c *reply.Config) { c.Language = "en"; c.Ratios = map[string]float64{"en": -1} },
	} {
		c := config()
		edit(&c)
		if _, err := reply.NewFilter(c); !errors.Is(err, reply.ErrConfig) {
			t.Fatalf("config accepted: %+v %v", c, err)
		}
	}
	f := filter(t, config())
	_, _ = f.Push("ok")
	if _, err := f.Push(string([]byte{0xff})); !errors.Is(err, reply.ErrUTF8) || f.FullText() != "ok" {
		t.Fatalf("invalid UTF-8: %v", err)
	}
}
