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
	Notification string
	Theme        string
	Profile      Profile
	ASCII        bool
	Width        int
	Language     design.Language
}

// Connection is a display state. Quit is a terminal transition, not a ninth
// connected state. Only Live permits mutations.
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

func (state Connection) active() bool {
	switch state {
	case Connecting, Live, Resyncing, Retrying, Recovering:
		return true
	case Refused, Stopped, Failed, Quit:
		return false
	}
	return false
}

func (state Connection) Code() string {
	switch state {
	case Connecting:
		return "connecting"
	case Live:
		return "live"
	case Resyncing:
		return "resyncing"
	case Retrying:
		return "retrying"
	case Refused:
		return "refused"
	case Recovering:
		return "recovering"
	case Stopped:
		return "stopped"
	case Failed:
		return "failed"
	case Quit:
		return "quit"
	}
	return "unknown"
}

func (state Connection) CanMutate() bool { return state == Live }

func connectionView(state Connection, t design.Tokens, ascii bool) connectionPresentation {
	return localizedConnectionView(state, t, ascii, design.English)
}

func localizedConnectionView(state Connection, t design.Tokens, ascii bool, language design.Language) connectionPresentation {
	unicode, plain := t.GlyphUnicodeUnknown, t.GlyphAsciiUnknown
	switch state {
	case Connecting:
		unicode, plain = t.GlyphUnicodeConnecting, t.GlyphAsciiConnecting
	case Live:
		unicode, plain = t.GlyphUnicodeLive, t.GlyphAsciiLive
	case Resyncing:
		unicode, plain = t.GlyphUnicodeResyncing, t.GlyphAsciiResyncing
	case Retrying:
		unicode, plain = t.GlyphUnicodeRetrying, t.GlyphAsciiRetrying
	case Refused:
		unicode, plain = t.GlyphUnicodeRefused, t.GlyphAsciiRefused
	case Recovering:
		unicode, plain = t.GlyphUnicodeRecovering, t.GlyphAsciiRecovering
	case Stopped:
		unicode, plain = t.GlyphUnicodeStopped, t.GlyphAsciiStopped
	case Failed:
		unicode, plain = t.GlyphUnicodeFailed, t.GlyphAsciiFailed
	case Quit:
		unicode, plain = t.GlyphUnicodeQuit, t.GlyphAsciiQuit
	}
	if ascii {
		unicode = plain
	}
	view := connectionPresentation{Mark: unicode, Label: design.Text(language, state.Code()), Detail: design.Text(language, "connection."+state.Code())}
	switch state {
	case Live, Resyncing, Retrying, Recovering:
		view.Action = design.Text(language, "action.quit")
	case Connecting, Refused, Stopped, Failed, Quit:
	}
	if state == Live {
		view.Detail = ""
	}
	return view
}

// Styles converts generated semantic data into Lip Gloss styles in one place.
type Styles struct {
	tokens                                   design.Tokens
	options                                  Options
	Text, Muted, Accent, Title, Canvas, Code lipgloss.Style
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
	s.Code = s.Text
	if options.Profile != Monochrome {
		s.Code = s.Code.Background(paint(tokens.ColorSurface))
	}
	s.Canvas = base
	return s
}

// Screen keeps a single reading edge and one blank row between groups.
func (s Styles) Screen(state Connection) string {
	return s.ScreenStatus(ConnectionStatus{State: state})
}

func (s Styles) ScreenStatus(connection ConnectionStatus) string {
	t := s.tokens
	view := s.presentation(connection)
	mark := t.GlyphUnicodeMark
	if s.options.ASCII {
		mark = t.GlyphAsciiMark
	}
	gutter := min(t.SpaceGutter.Cells, max(0, (s.options.Width-1)/2))
	width := max(1, s.options.Width-2*gutter)
	gap := strings.Repeat(" ", t.SpaceInline.Cells)
	section := strings.Repeat("\n", t.SpaceSection.Cells+1)
	title := s.Title.Render("lina") + s.Accent.Bold(s.Title.GetBold()).Render(mark)
	stateStyle := s.Muted
	if connection.State.active() {
		stateStyle = s.Accent
	}
	status := stateStyle.Render(view.Mark+gap) + s.Text.Render(view.Label)
	body := title + section + status
	if view.Detail != "" {
		detail := s.Muted.Width(width).PaddingLeft(min(width-1, ansi.StringWidth(view.Mark)+t.SpaceInline.Cells)).Render(view.Detail)
		body += "\n" + detail
	}
	if view.Action != "" {
		body += section + s.Muted.Render("[") + s.Accent.Render("q") + s.Muted.Render("]"+gap) + s.Muted.Render(view.Action)
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
	locale := getenv("LC_ALL")
	if locale == "" {
		locale = getenv("LC_MESSAGES")
	}
	if locale == "" {
		locale = getenv("LANG")
	}
	opts.Language = design.LanguageFor(locale)
	switch getenv("LINA_NOTIFICATIONS") {
	case "bell", "terminal":
		opts.Notification = getenv("LINA_NOTIFICATIONS")
	}
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
