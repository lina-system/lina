// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
)

// Profile describes the output capability, independently of the chosen theme.
type Profile uint8

const (
	Monochrome Profile = iota
	ANSI16
	ANSI256
	TrueColor
)

// Options are ephemeral display preferences. They never change Core state.
type Options struct {
	Theme   string
	Profile Profile
	ASCII   bool
	Width   int
}

// Connection is a presentation state, not a claim about the protocol client.
// Future connection handling can use the same vocabulary and styles.
type Connection uint8

const (
	Connecting Connection = iota
	Live
	Resyncing
	Retrying
	Refused
	Recovering
	Stopped
	Failed
	Quit
)

type connectionPresentation struct{ Mark, Label, Detail, Action string }

func connectionView(state Connection, t design.Tokens, ascii bool) connectionPresentation {
	glyph := func(unicode, plain string) string {
		if ascii {
			return plain
		}
		return unicode
	}
	switch state {
	case Connecting:
		return connectionPresentation{glyph(t.GlyphUnicodeConnecting, t.GlyphAsciiConnecting), "Connecting", "Opening your connection to LINA.", ""}
	case Live:
		return connectionPresentation{glyph(t.GlyphUnicodeLive, t.GlyphAsciiLive), "Connected", "Your connection is ready.", "q + Enter  Quit"}
	case Resyncing:
		return connectionPresentation{glyph(t.GlyphUnicodeResyncing, t.GlyphAsciiResyncing), "Syncing", "Bringing your conversation up to date.", "q + Enter  Quit"}
	case Retrying:
		return connectionPresentation{glyph(t.GlyphUnicodeRetrying, t.GlyphAsciiRetrying), "Reconnecting", "Your conversation stays here while the connection returns.", "q + Enter  Quit"}
	case Refused:
		return connectionPresentation{glyph(t.GlyphUnicodeRefused, t.GlyphAsciiRefused), "Connection refused", "Check that LINA and Core are compatible.", ""}
	case Recovering:
		return connectionPresentation{glyph(t.GlyphUnicodeRecovering, t.GlyphAsciiRecovering), "Recovering", "You can read while Core recovers.", "q + Enter  Quit"}
	case Stopped:
		return connectionPresentation{glyph(t.GlyphUnicodeStopped, t.GlyphAsciiStopped), "Core is unavailable", "Start Core, then connect again.", ""}
	case Failed:
		return connectionPresentation{glyph(t.GlyphUnicodeFailed, t.GlyphAsciiFailed), "Connection failed", "Check Core and your terminal, then connect again.", ""}
	case Quit:
		return connectionPresentation{glyph(t.GlyphUnicodeQuit, t.GlyphAsciiQuit), "Disconnected", "LINA keeps running. See you soon.", ""}
	}
	return connectionPresentation{glyph(t.GlyphUnicodeUnknown, t.GlyphAsciiUnknown), "Unknown", "The connection state is unknown.", ""}
}

// Styles converts generated semantic data into Lip Gloss styles in one place.
type Styles struct {
	tokens                                   design.Tokens
	options                                  Options
	Text, Muted, Accent, Title, Canvas, Rule lipgloss.Style
}

func NewStyles(options Options) Styles {
	tokens := design.Dark()
	switch options.Theme {
	case "light":
		tokens = design.Light()
	case "high-contrast":
		tokens = design.HighContrast()
	}
	if options.Width <= 0 {
		options.Width = 80
	}
	s := Styles{tokens: tokens, options: options}
	base := lipgloss.NewStyle()
	paint := func(c design.Color) color.Color {
		switch options.Profile {
		case Monochrome:
			return lipgloss.NoColor{}
		case ANSI16:
			return lipgloss.Color(strconv.Itoa(c.ANSI16))
		case ANSI256:
			return lipgloss.Color(strconv.Itoa(c.ANSI256))
		case TrueColor:
			return lipgloss.Color(c.Hex)
		}
		return lipgloss.NoColor{}
	}
	if options.Profile != Monochrome {
		base = base.Background(paint(tokens.ColorCanvas))
	}
	s.Text = base.Foreground(paint(tokens.ColorText))
	s.Muted = base.Foreground(paint(tokens.ColorMuted))
	s.Accent = base.Foreground(paint(tokens.ColorAccent))
	s.Title = s.Text.Bold(tokens.TypeTitleWeight > tokens.TypeWeight && options.Profile != Monochrome)
	s.Canvas = base
	s.Rule = base.Foreground(paint(tokens.ColorLine))
	return s
}

// Screen keeps a single reading edge, a small mark and one active action.
func (s Styles) Screen(state Connection) string {
	t := s.tokens
	view := connectionView(state, t, s.options.ASCII)
	mark, rule, action := t.GlyphUnicodeMark, t.GlyphUnicodeRule, t.GlyphUnicodeAction
	if s.options.ASCII {
		mark, rule, action = t.GlyphAsciiMark, t.GlyphAsciiRule, t.GlyphAsciiAction
	}
	gutter := min(t.SpaceGutter.Cells, max(0, (s.options.Width-1)/2))
	width := max(1, s.options.Width-2*gutter)
	gap := strings.Repeat(" ", t.SpaceInline.Cells)
	section := strings.Repeat("\n", t.SpaceSection.Cells+1)
	title := s.Title.Render("lina") + s.Accent.Render(mark)
	status := s.Text.Render(view.Mark+gap) + s.Title.Render(view.Label)
	detail := s.Muted.Width(width).PaddingLeft(min(width-1, ansi.StringWidth(view.Mark)+t.SpaceInline.Cells)).Render(view.Detail)
	body := title + section + "\n" + status + "\n" + detail
	if view.Action != "" {
		body += section + s.Rule.Render(strings.Repeat(rule, width)) + section + s.Accent.Render(action+gap+view.Action)
	}
	// Clip after styling and wrap words through Lip Gloss before padding. No
	// terminal-sized frame or fixed card width can spill at a narrow viewport.
	body = s.Canvas.Width(width).Render(body)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	return s.Canvas.Width(s.options.Width).Padding(t.SpacePage.Cells, gutter).Render(strings.Join(lines, "\n")) + "\n"
}

func environmentOptions(getenv func(string) string) Options {
	opts := Options{Theme: "dark", Profile: ANSI16, ASCII: getenv("LINA_GLYPHS") == "ascii", Width: 80}
	background := strings.Split(getenv("COLORFGBG"), ";")
	if last, err := strconv.Atoi(background[len(background)-1]); err == nil && (last == 7 || last == 15) {
		opts.Theme = "light"
	}
	switch getenv("LINA_THEME") {
	case "light", "dark", "high-contrast":
		opts.Theme = getenv("LINA_THEME")
	}
	switch {
	case getenv("NO_COLOR") != "":
		opts.Profile = Monochrome
	case getenv("COLORTERM") == "truecolor" || getenv("COLORTERM") == "24bit":
		opts.Profile = TrueColor
	case strings.Contains(getenv("TERM"), "256color"):
		opts.Profile = ANSI256
	}
	return opts
}
