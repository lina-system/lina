// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type display struct {
	output      io.Writer
	styles      Styles
	interactive bool
	rawOutput   bool
	rows        int
}

func (d *display) write(text string) error {
	// Raw input disables the terminal's newline processing as well.
	if d.rawOutput {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	_, err := io.WriteString(d.output, text)
	return err
}

func newDisplay(output io.Writer) *display {
	d := &display{output: output}
	file, ok := output.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(file.Fd()) || os.Getenv("TERM") == "" || os.Getenv("TERM") == "dumb" {
		return d
	}
	opts := environmentOptions(os.Getenv)
	if width, _, err := term.GetSize(file.Fd()); err == nil && width > 0 {
		opts.Width = width
	}
	d.styles = NewStyles(opts)
	d.interactive = true
	return d
}

func (d *display) show(state Connection) error {
	if !d.interactive {
		if state == Live {
			return d.write("live\n")
		}
		return nil
	}
	frame := d.styles.Screen(state)
	rows := strings.Count(frame, "\n")
	// NO_COLOR emits no escapes at all. After the initial brand panel, append
	// only the new labelled state so a monochrome transcript remains compact.
	if d.rows > 0 {
		if d.styles.options.Profile == Monochrome {
			v := connectionView(state, d.styles.tokens, d.styles.options.ASCII)
			gutter := strings.Repeat(" ", d.styles.tokens.SpaceGutter.Cells)
			text := gutter + v.Mark + " " + v.Label
			if v.Detail != "" {
				text += "\n" + gutter + v.Detail
			}
			if v.Action != "" {
				text += "\n" + gutter + "[q] " + v.Action
			}
			return d.write(text + "\n")
		}
		frame = ansi.CursorUp(d.rows) + ansi.EraseScreenBelow + frame
	}
	err := d.write(frame)
	d.rows = rows
	return err
}
