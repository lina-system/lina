// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/state"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func TestShellBackgroundCoversEmptyAndTextRows(t *testing.T) {
	for _, profile := range []Profile{ANSI16, ANSI256, TrueColor} {
		for _, theme := range []string{"", "light", "high-contrast"} {
			t.Run(fmt.Sprintf("profile=%d/theme=%s", profile, theme), func(t *testing.T) {
				env := map[string]string{"TERM": "xterm", "LINA_THEME": theme}
				if profile == ANSI256 {
					env["TERM"] = "xterm-256color"
				} else if profile == TrueColor {
					env["COLORTERM"] = "truecolor"
				}
				opts := environmentOptions(func(key string) string { return env[key] })
				m := conversationShell()
				m.styles = NewStyles(opts)
				m.conversation = newConversationView(m.styles)
				m.conversation.id = "main:fixture"
				m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: []state.Block{{ID: "reply", Text: "Background fixture", Done: true, Revision: 1}}}})
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				output, err := tuiterm.NewRecorder(80, 24)
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 80, 24, opts.Profile)
				defer renderer.close()
				// Include a resize and a short connection view, whose content does
				// not itself occupy the terminal's full height.
				for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 100, Height: 30}, {Width: 32, Height: 8}} {
					if err := output.Resize(size.Width, size.Height); err != nil {
						t.Fatal(err)
					}
					m.Update(size)
					for _, connection := range []Connection{Live, Retrying, Failed} {
						m.status.State = connection
						if err := renderer.showFlushed(m.View(), size); err != nil {
							t.Fatal(err)
						}
						snapshot := output.Snapshot()
						// Use a separately rendered semantic token as the expected
						// colour, so profile conversion is part of the assertion.
						reference, err := tuiterm.NewRecorder(1, 1)
						if err != nil {
							t.Fatal(err)
						}
						_, err = reference.Write([]byte(m.styles.Text.Render("x")))
						want := reference.Snapshot().Cells[0][0].Background
						reference.Close()
						if err != nil || want == nil {
							t.Fatalf("reference background = %v, error = %v", want, err)
						}
						empty, text := false, false
						for y, row := range snapshot.Cells {
							var line strings.Builder
							for x, cell := range row {
								line.WriteString(cell.Content)
								if !reflect.DeepEqual(cell.Background, want) {
									t.Fatalf("%dx%d %s cell (%d,%d) %q background = %v, want %v", size.Width, size.Height, connection.Code(), x, y, cell.Content, cell.Background, want)
								}
							}
							if strings.TrimSpace(line.String()) == "" {
								empty = true
							} else {
								text = true
							}
						}
						if !empty || !text {
							t.Fatalf("fixture must exercise empty and text rows: empty=%t text=%t", empty, text)
						}
					}
				}
			})
		}
	}
}

func TestViewportPreservesSurfaceAndRestoresDefaults(t *testing.T) {
	styles := NewStyles(Options{Profile: TrueColor})
	// Exercise full and individual resets, including a reset combined with
	// another attribute. Explicit code backgrounds and hyperlinks survive.
	content := styles.Code.Render("C") + "\x1b[1;49mB\x1b[39mF\x1b[0mD\x1b[mE" + ansi.SetHyperlink("https://example.com") + "L" + ansi.ResetHyperlink()
	output, err := tuiterm.NewRecorder(12, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	frame := styles.viewport(content, 12, 2)
	if _, err := output.Write([]byte(strings.ReplaceAll(frame, "\n", "\r\n"))); err != nil {
		t.Fatal(err)
	}
	// Compare through the same terminal profile conversion as the actual view.
	reference, err := tuiterm.NewRecorder(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Close()
	if _, err := reference.Write([]byte(styles.Code.Render("C") + styles.Text.Render("T"))); err != nil {
		t.Fatal(err)
	}
	want := reference.Snapshot().Cells[0]
	snapshot := output.Snapshot()
	for y, row := range snapshot.Cells {
		for x, cell := range row {
			background := want[1].Background
			if x == 0 && y == 0 {
				background = want[0].Background
			}
			if !reflect.DeepEqual(cell.Background, background) {
				t.Errorf("cell (%d,%d) background = %v, want %v", x, y, cell.Background, background)
			}
			if y == 0 && x < 6 && !reflect.DeepEqual(cell.Foreground, want[1].Foreground) {
				t.Errorf("cell (%d,%d) lost default foreground", x, y)
			}
		}
	}
	if !snapshot.Cells[0][1].Bold || snapshot.Cells[0][3].Bold {
		t.Fatal("canvas changed nested text attributes")
	}
	if !strings.Contains(frame, ansi.SetHyperlink("https://example.com")) || !strings.Contains(frame, ansi.ResetHyperlink()) {
		t.Fatal("canvas changed the hyperlink")
	}
}

func TestMonochromeViewportKeepsPlainOutput(t *testing.T) {
	styles := NewStyles(Options{Profile: Monochrome})
	const content = "plain\n\ntext"
	if got := styles.viewport(content, 80, 24); got != content {
		t.Fatalf("monochrome output changed: %q", got)
	}
}
