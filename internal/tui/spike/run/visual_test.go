// SPDX-License-Identifier: Apache-2.0

package run

import (
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
	supported := probe.setting == 1 || probe.setting == 2 || probe.setting == 3
	values := map[string]any{"grapheme_supported": supported, "mode_2027_report": probe.setting, "mode_2027_probe_ms": probe.elapsed}
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
		if mode.width == render.Graphemes && !supported {
			values["mitigation"] = "Keep wcwidth on this terminal. Exercise mode 2027 on a terminal that confirms it before accepting both width modes; use the own-table renderer on a Glamour alignment miss."
			break
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
			if probe.setting == 1 || probe.setting == 2 {
				suffix := "l"
				if mode.width == render.Graphemes {
					suffix = "h"
				}
				if _, err := fmt.Fprintf(s.output, "\x1b[?2027%s", suffix); err != nil {
					return err
				}
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

var _ io.Writer = (*recordingOutput)(nil)
