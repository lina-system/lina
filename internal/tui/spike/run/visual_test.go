// SPDX-License-Identifier: Apache-2.0

package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"github.com/thisisjun786/lina/internal/tui/render"
)

var tableCells = [][]string{{"종류", "value", "emoji"}, {"한글", "Latin", "👩‍💻"}, {"혼합 A", "é", "🇰🇷"}, {"끝", "123", "🙂"}}

const tableMarkdown = "| 종류 | value | emoji |\n| --- | --- | --- |\n| 한글 | Latin | 👩‍💻 |\n| 혼합 A | é | 🇰🇷 |\n| 끝 | 123 | 🙂 |\n"

func ownTable(mode render.WidthMode) string {
	m := render.Unicode{WidthMode: mode}
	widths := make([]int, 3)
	for _, row := range tableCells {
		for col, text := range row {
			widths[col] = max(widths[col], m.Width(text))
		}
	}
	var lines []string
	for _, row := range tableCells {
		var line strings.Builder
		line.WriteString("|")
		for col, text := range row {
			line.WriteString(" " + text + strings.Repeat(" ", widths[col]-m.Width(text)) + " |")
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

func runTables(ctx context.Context, q *observations) (map[string]any, error) {
	pump, err := rawInput(ctx)
	if err != nil {
		return nil, err
	}
	probe, err := probeMode(ctx, pump, os.Stdout, 2027)
	pump.close()
	if err != nil {
		return nil, err
	}
	values := map[string]any{"mode_2027_report": probe.setting, "mode_2027_probe_ms": probe.elapsed}
	// Restore the observed initial mode even when the operator cancels a screen.
	if probe.setting == 1 || probe.setting == 2 {
		defer func() {
			suffix := "l"
			if probe.setting == 1 {
				suffix = "h"
			}
			fmt.Fprintf(os.Stdout, "\x1b[?2027%s", suffix)
		}()
	}
	painter, err := glamour.NewTermRenderer(glamour.WithStandardStyle("notty"), glamour.WithWordWrap(0))
	if err != nil {
		return nil, err
	}
	painted, err := painter.Render(tableMarkdown)
	if err != nil {
		return nil, err
	}
	ownSelected := false
	for _, mode := range []struct {
		name  string
		width render.WidthMode
	}{{"wcwidth", render.WCWidth}, {"grapheme", render.Graphemes}} {
		// Select the width mode before the renderer can paint its initial frame.
		supported, err := prepareTableMode(os.Stdout, probe.setting, mode.width)
		if err != nil {
			return nil, err
		}
		values[mode.name+"_supported"] = supported
		if !supported {
			values["mitigation"] = "Compare both width modes on a terminal that allows mode 2027 to be toggled; a permanently set mode cannot establish wcwidth alignment. Use the own-table renderer on a Glamour alignment miss."
			continue
		}
		content := mode.name + "; inspect the same cell boundaries in both tables. Ctrl+D continues.\nGlamour:\n" + painted + "\nOwn table renderer:\n" + ownTable(mode.width)
		s, err := startSession(ctx, &screen{text: content}, os.Stdin)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer s.close()
			if s.height < 20 {
				return errors.New("S10 requires at least 20 rows to inspect both tables")
			}
			s.program.Send(frameMsg{text: content, sequence: 1})
			if err := s.output.waitFrame(ctx, 1); err != nil {
				return err
			}
			return s.wait()
		}()
		if err != nil {
			return nil, err
		}
		aligned, err := q.yes(mode.name + ": all Glamour column boundaries aligned for Hangul, Latin, combining marks and emoji")
		if err != nil {
			return nil, err
		}
		values["glamour_"+mode.name+"_aligned"] = aligned
		ownSelected = ownSelected || !aligned
		values["own_"+mode.name+"_aligned"], err = q.yes(mode.name + ": all own-renderer column boundaries aligned")
		if err != nil {
			return nil, err
		}
	}
	values["own_tables_selected"] = ownSelected
	values["method"] = "Owner compares physical columns; mode 2027 is enabled only after a supporting DECRPM reply and its initial state is restored. Unsupported grapheme mode is recorded as a miss, never inferred from width arithmetic."
	return values, nil
}

// prepareTableMode reports whether this physical mode can be observed and
// switches mutable modes before any table cells are written.
func prepareTableMode(output io.Writer, setting int, mode render.WidthMode) (bool, error) {
	if setting == 1 || setting == 2 {
		suffix := "l"
		if mode == render.Graphemes {
			suffix = "h"
		}
		_, err := fmt.Fprintf(output, "\x1b[?2027%s", suffix)
		return err == nil, err
	}
	if setting == 3 {
		return mode == render.Graphemes, nil
	}
	return mode == render.WCWidth, nil
}

func paneContent(width int) string {
	pane := width / 3
	conversation := width - pane - 3
	left := []string{"Conversation", "You: Compare the synthetic quotes.", "Lina: Quote B costs less.", "[table] Quote comparison", "o Open   s Save", "", "> Composer: draft stays visible"}
	right := []string{"Quote comparison", "item     A      B", "price   120    100", "delivery  3      4", "Open   Save", "", "Return to conversation"}
	m := render.Unicode{}
	var lines []string
	for i := range left {
		l := render.Truncate(left[i], conversation, m)
		r := render.Truncate(right[i], pane, m)
		lines = append(lines, l+strings.Repeat(" ", max(0, conversation-m.Width(l)))+" | "+r)
	}
	return fmt.Sprintf("Width %d of 100..140. Inspect both regions, actions and composer.\ny: usable, n: unusable; each answer advances one column.\nOnly answer after inspecting the newly painted width.\n\n", width) + strings.Join(lines, "\n")
}
func runPane(ctx context.Context, q *observations) (map[string]any, error) {
	width := 100
	values := map[string]any{}
	complete := false
	model := &screen{text: paneContent(width)}
	model.onMessage = func(m *screen, message tea.Msg) tea.Cmd {
		key, ok := message.(tea.KeyPressMsg)
		if !ok || key.IsRepeat || (key.Code != 'y' && key.Code != 'n') {
			return nil
		}
		m.writer.mu.Lock()
		painted := m.writer.lastFrame >= m.sequence
		m.writer.mu.Unlock()
		if !painted {
			return nil
		}
		values[fmt.Sprintf("width_%d_usable", width)] = key.Code == 'y'
		if width == 140 {
			complete = true
			return tea.Quit
		}
		width++
		m.sequence++
		m.text = paneContent(width)
		return nil
	}
	s, err := startSession(ctx, model, os.Stdin)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if s.width < 140 || s.height < 16 {
		return nil, errors.New("S11 needs at least 140 columns and 16 rows; it draws every candidate width inside that terminal")
	}
	if err := s.wait(); err != nil {
		return nil, err
	}
	if !complete {
		return nil, errors.New("inspect all widths 100 through 140 before saving the threshold")
	}
	threshold := 0
	for w := 100; w <= 140; w++ {
		if values[fmt.Sprintf("width_%d_usable", w)].(bool) {
			threshold = w
			break
		}
	}
	values["threshold_found"] = threshold != 0
	if threshold != 0 {
		values["threshold_columns"] = threshold
		fmt.Fprintf(q.output, "First observed usable width: %d columns (current default: 120).\n", threshold)
	}
	values["threshold_reason"], err = q.line("Why does this threshold keep the pane usable (or why is none usable)")
	if err != nil {
		return nil, err
	}
	values["method"] = "One observed y/n decision for every width 100..140; keys are admitted only after the recorder observes that width's frame."
	return values, nil
}

func TestOwnTableCellBoundaries(t *testing.T) {
	for _, mode := range []render.WidthMode{render.WCWidth, render.Graphemes} {
		m := render.Unicode{WidthMode: mode}
		var expected []int
		for _, line := range strings.Split(ownTable(mode), "\n") {
			var columns []int
			for i, r := range line {
				if r == '|' {
					columns = append(columns, m.Width(line[:i]))
				}
			}
			if expected == nil {
				expected = columns
			}
			if fmt.Sprint(columns) != fmt.Sprint(expected) {
				t.Fatalf("mode %d: columns %v differ from %v", mode, columns, expected)
			}
		}
	}
}

func TestTableModeSelection(t *testing.T) {
	for _, tc := range []struct {
		setting   int
		mode      render.WidthMode
		supported bool
		output    string
	}{
		{1, render.WCWidth, true, "\x1b[?2027l"},
		{1, render.Graphemes, true, "\x1b[?2027h"},
		{2, render.WCWidth, true, "\x1b[?2027l"},
		{2, render.Graphemes, true, "\x1b[?2027h"},
		{3, render.WCWidth, false, ""},
		{3, render.Graphemes, true, ""},
		{4, render.WCWidth, true, ""},
		{4, render.Graphemes, false, ""},
		{0, render.WCWidth, true, ""},
		{0, render.Graphemes, false, ""},
	} {
		var output bytes.Buffer
		got, err := prepareTableMode(&output, tc.setting, tc.mode)
		if err != nil || got != tc.supported || output.String() != tc.output {
			t.Fatalf("setting %d, mode %d: supported=%v output=%q error=%v", tc.setting, tc.mode, got, output.String(), err)
		}
	}
}

var _ io.Writer = (*recordingOutput)(nil)
