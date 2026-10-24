// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"image/color"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
)

func TestSemanticPaletteProfiles(t *testing.T) {
	for _, theme := range []string{"light", "dark", "high-contrast"} {
		for _, profile := range []Profile{Monochrome, ANSI16, ANSI256, TrueColor} {
			s := NewStyles(Options{Theme: theme, Profile: profile})
			tokens := s.tokens
			paint := func(c design.Color) color.Color {
				switch profile {
				case ANSI16:
					return lipgloss.Color(strconv.Itoa(c.ANSI16))
				case ANSI256:
					return lipgloss.Color(strconv.Itoa(c.ANSI256))
				case TrueColor:
					return lipgloss.Color(c.Hex)
				case Monochrome:
					return lipgloss.NoColor{}
				}
				return lipgloss.NoColor{}
			}
			for _, tc := range []struct {
				name   string
				style  lipgloss.Style
				fg, bg design.Color
			}{
				{"success", s.Success, tokens.ColorSuccess, tokens.ColorCanvas},
				{"warning", s.Warning, tokens.ColorWarning, tokens.ColorCanvas},
				{"error", s.Error, tokens.ColorError, tokens.ColorCanvas},
				{"unknown", s.Unknown, tokens.ColorUnknown, tokens.ColorCanvas},
				{"working", s.Working, tokens.ColorWorking, tokens.ColorCanvas},
				{"waiting", s.Waiting, tokens.ColorWaiting, tokens.ColorCanvas},
				{"disabled", s.Disabled, tokens.ColorDisabled, tokens.ColorCanvas},
				{"focus", s.Focus, tokens.ColorFocus, tokens.ColorCanvas},
				{"border", s.Border, tokens.ColorBorder, tokens.ColorCanvas},
				{"selection", s.Selection, tokens.ColorOnSelection, tokens.ColorSelection},
				{"onAccent", s.OnAccent, tokens.ColorOnAccent, tokens.ColorAccent},
				{"diffAdded", s.DiffAdded, tokens.ColorDiffAdded, tokens.ColorDiffAddedBackground},
				{"diffRemoved", s.DiffRemoved, tokens.ColorDiffRemoved, tokens.ColorDiffRemovedBackground},
			} {
				if !reflect.DeepEqual(tc.style.GetForeground(), paint(tc.fg)) {
					t.Errorf("%s/%d/%s: wrong foreground", theme, profile, tc.name)
				}
				if profile != Monochrome && !reflect.DeepEqual(tc.style.GetBackground(), paint(tc.bg)) {
					t.Errorf("%s/%d/%s: wrong background", theme, profile, tc.name)
				}
				out := tc.style.Render("? Unknown + added - removed")
				if profile == Monochrome && strings.Contains(out, "\x1b") {
					t.Errorf("%s/%s: NO_COLOR has escape styling", theme, tc.name)
				}
			}
		}
	}
}

func TestSharedIconsAndToolRows(t *testing.T) {
	for _, tokens := range []design.Tokens{design.Light(), design.Dark(), design.HighContrast()} {
		for _, role := range []string{"citation", "success", "error", "working", "waiting", "unknown", "memory", "identity"} {
			for _, ascii := range []bool{false, true} {
				mark := tokens.Icon(role, ascii)
				if mark == "" || strings.Contains(mark, "\x1b") {
					t.Fatalf("missing or unsafe icon %s", role)
				}
				if ascii && strings.IndexFunc(mark, func(r rune) bool { return r < 32 || r > 126 }) >= 0 {
					t.Fatalf("non-ASCII fallback for %s: %q", role, mark)
				}
				if tokens.Icon("unrecognized", ascii) != tokens.Icon("unknown", ascii) {
					t.Fatal("unknown role lost its visible mark")
				}
			}
		}
	}
	for _, ascii := range []bool{false, true} {
		m := toolShell(t)
		m.styles.options.ASCII = ascii
		// Distinguish the shared icon from the retained legacy tool glyph.
		m.styles.tokens.GlyphUnicodeIconWorking = "shared-running"
		m.styles.tokens.GlyphAsciiIconWorking = "shared-ascii-running"
		applyToolProjection(t, m, toolTurn(t, 1, "tool", "Reading"))
		applyToolProjection(t, m, rowEvent(t, 2, "one", "Reading file", "started", "", "", ""))
		if !strings.Contains(m.View().Content, m.styles.tokens.Icon("working", ascii)) {
			t.Fatal("tool row did not consume the shared icon source")
		}
	}
}

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
