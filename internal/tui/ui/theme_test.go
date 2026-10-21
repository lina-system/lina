// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"github.com/charmbracelet/x/ansi"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
)

func TestThemeProfiles(t *testing.T) {
	for _, theme := range []string{"light", "dark", "high-contrast"} {
		for _, profile := range []Profile{Monochrome, ANSI16, ANSI256, TrueColor} {
			for _, ascii := range []bool{false, true} {
				opts := Options{Theme: theme, Profile: profile, ASCII: ascii, Width: 72}
				styles := NewStyles(opts)
				for _, state := range []Connection{Connecting, Live, Resyncing, Retrying, Refused, Recovering, Stopped, Failed, Quit} {
					model := connectionView(state, styles.tokens, ascii)
					if model.Mark == "" || model.Label == "" {
						t.Fatalf("unlabelled state: %v", state)
					}
					output := styles.Screen(state)
					if profile == Monochrome && strings.Contains(output, "\x1b") {
						t.Fatal("NO_COLOR contains escapes")
					}
					if profile == ANSI16 && (strings.Contains(output, "38;2;") || strings.Contains(output, "38;5;")) {
						t.Fatal("16-colour output exceeds profile")
					}
					if profile == ANSI256 && strings.Contains(output, "38;2;") {
						t.Fatal("256-colour output exceeds profile")
					}
					if ascii {
						for _, r := range output {
							if r > 127 {
								t.Fatalf("non-ASCII output: %U", r)
							}
						}
					}
				}
			}
		}
	}
}

func TestConnectionMarksAreDistinct(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		seen := map[string]bool{}
		for _, state := range []Connection{Connecting, Live, Resyncing, Retrying, Refused, Recovering, Stopped, Failed, Quit} {
			view := connectionView(state, design.Dark(), ascii)
			if seen[view.Mark] {
				t.Fatalf("shared mark at %v", state)
			}
			seen[view.Mark] = true
		}
	}
}

func TestConnectionEmphasis(t *testing.T) {
	styles := NewStyles(Options{Profile: TrueColor, Width: 72})
	for _, state := range []Connection{Connecting, Live, Resyncing, Retrying, Refused, Recovering, Stopped, Failed, Quit} {
		view := connectionView(state, styles.tokens, false)
		output := styles.Screen(state)
		markStyle := styles.Muted
		switch state {
		case Connecting, Live, Resyncing, Retrying, Recovering:
			markStyle = styles.Accent
		case Refused, Stopped, Failed, Quit:
			markStyle = styles.Muted
		}
		if !strings.Contains(output, markStyle.Render(view.Mark+" ")) {
			t.Errorf("state %d: mark does not convey active versus inactive", state)
		}
		if !strings.Contains(output, styles.Text.Render(view.Label)) {
			t.Errorf("state %d: label competes with the wordmark", state)
		}
		if view.Action != "" && !strings.Contains(output, styles.Muted.Render(view.Action)) {
			t.Errorf("state %d: action label is not secondary text", state)
		}
	}
}

func TestDisplayEnvironment(t *testing.T) {
	for _, tc := range []struct {
		env     map[string]string
		profile Profile
		theme   string
		ascii   bool
	}{
		{map[string]string{"TERM": "xterm", "NO_COLOR": "1", "COLORTERM": "truecolor"}, Monochrome, "dark", false},
		{map[string]string{"TERM": "xterm", "COLORTERM": "truecolor"}, TrueColor, "dark", false},
		{map[string]string{"TERM": "xterm-256color", "COLORFGBG": "0;15"}, ANSI256, "light", false},
		{map[string]string{"TERM": "vt100", "LINA_THEME": "high-contrast", "LINA_GLYPHS": "ascii"}, ANSI16, "high-contrast", true},
		{map[string]string{"TERM": "xterm", "LINA_THEME": "invalid"}, ANSI16, "dark", false},
	} {
		opts := environmentOptions(func(key string) string { return tc.env[key] })
		if opts.Profile != tc.profile || opts.Theme != tc.theme || opts.ASCII != tc.ascii {
			t.Fatalf("options = %+v", opts)
		}
	}
}

func TestMonochromeTransitionsKeepActions(t *testing.T) {
	var output strings.Builder
	d := display{output: &output, styles: NewStyles(Options{Profile: Monochrome, Width: 72}), interactive: true}
	for _, state := range []Connection{Connecting, Live, Quit} {
		output.Reset()
		if err := d.show(state); err != nil {
			t.Fatal(err)
		}
		view := connectionView(state, d.styles.tokens, false)
		if !strings.Contains(output.String(), view.Mark) || !strings.Contains(output.String(), view.Label) || !strings.Contains(output.String(), view.Action) {
			t.Fatalf("transition %d lost its mark, label or action", state)
		}
	}
}

func TestCanvasCoversEveryCellAndFitsWidth(t *testing.T) {
	sgr := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	for _, theme := range []string{"dark", "light", "high-contrast"} {
		for _, width := range []int{1, 12, 32, 72, 120} {
			styles := NewStyles(Options{Theme: theme, Profile: TrueColor, Width: width})
			output := styles.Screen(Live)
			for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("%s width %d overflows", theme, width)
				}
			}
			bg := ""
			for len(output) > 0 {
				loc := sgr.FindStringSubmatchIndex(output)
				end := len(output)
				if loc != nil {
					end = loc[0]
				}
				if strings.Trim(output[:end], "\n") == "" {
				} else if bg != "canvas" {
					t.Fatalf("%s width %d leaves cells on terminal background: %q", theme, width, output[:end])
				}
				if loc == nil {
					break
				}
				codes := strings.Split(output[loc[2]:loc[3]], ";")
				for i := 0; i < len(codes); i++ {
					switch codes[i] {
					case "", "0", "49":
						bg = ""
					case "48":
						if i+4 < len(codes) && codes[i+1] == "2" {
							expected := styles.tokens.ColorCanvas.Hex
							match := true
							for channel := 0; channel < 3; channel++ {
								n, _ := strconv.ParseInt(expected[1+channel*2:3+channel*2], 16, 32)
								match = match && codes[i+2+channel] == strconv.FormatInt(n, 10)
							}
							if match {
								bg = "canvas"
							} else {
								bg = "other"
							}
							i += 4
						}
					case "38":
						if i+4 < len(codes) && codes[i+1] == "2" {
							i += 4
						}
					}
				}
				output = output[loc[1]:]
			}
		}
	}
}
