// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"strings"
	"testing"
)

func TestWidthCorpus(t *testing.T) {
	for _, tc := range []struct {
		text             string
		cells, graphemes int
	}{
		{"한글", 4, 4}, {"A한B", 4, 4}, {"e\u0301", 1, 1}, {"·", 1, 1}, {"👩‍💻", 4, 2}, {"🇰🇷", 4, 2}, {"♥️", 1, 2}, {"한", 2, 2},
	} {
		for _, mode := range []WidthMode{WCWidth, Graphemes} {
			want := tc.cells
			if mode == Graphemes {
				want = tc.graphemes
			}
			m := Unicode{WidthMode: mode}
			if got := m.Width(tc.text); got != want {
				t.Errorf("%q mode=%v width=%d want=%d", tc.text, mode, got, want)
			}
		}
	}
}

func TestStreamChunkingAndSeal(t *testing.T) {
	input := "한글 é 👩‍💻 words\n\n```go\ncode\n```\n" + strings.Repeat("x", 300)
	for _, mode := range []WidthMode{WCWidth, Graphemes} {
		for width := 1; width <= 40; width++ {
			config := Config{Width: width, Mode: mode}
			whole := NewStream("item", config, Unicode{WidthMode: mode})
			whole.Seal(1, input)
			for _, chunk := range []int{1, 2, 7, 31} {
				stream := NewStream("item", config, Unicode{WidthMode: mode})
				rev := uint64(0)
				for pos := 0; pos < len(input); pos += chunk {
					rev++
					stream.Append(rev, input[pos:min(pos+chunk, len(input))])
				}
				if stream.Len() != whole.Len() {
					t.Fatalf("chunk=%d width=%d rows=%d want=%d", chunk, width, stream.Len(), whole.Len())
				}
				for i := 0; i < stream.Len(); i++ {
					row := stream.At(i)
					if row.Text != whole.At(i).Text {
						t.Fatalf("chunk=%d width=%d row=%d: %q want %q", chunk, width, i, row.Text, whole.At(i).Text)
					}
					if (Unicode{WidthMode: config.Mode}).Width(row.Text) > width {
						t.Fatal("row overflows")
					}
				}
				stream.Seal(rev+1, "authoritative\x1b]52;c;private\a")
				before := stream.At(0)
				stream.Seal(rev+1, "duplicate")
				stream.Append(rev+2, "late")
				if stream.At(0) != before {
					t.Fatal("sealed stream changed on replay")
				}
			}
		}
	}
}

type countingMeasurer struct {
	calls int
	Unicode
}

func (m *countingMeasurer) Width(s string) int { m.calls++; return m.Unicode.Width(s) }
func TestAppendDoesNotRemeasurePrefix(t *testing.T) {
	m := &countingMeasurer{}
	s := NewStream("item", Config{Width: 80}, m)
	s.Append(1, strings.Repeat("word ", 20000))
	m.calls = 0
	s.Append(2, "next")
	if m.calls > 200 {
		t.Fatalf("remeasured history: %d", m.calls)
	}
}

type history struct{ count, reads int }

func (h *history) Len() int { return h.count }
func (h *history) At(i int) Row {
	h.reads++
	return Row{ID: fmt.Sprint(i), Revision: 1, Text: "한글 content", Done: true}
}
func TestRendererWorkFollowsViewport(t *testing.T) {
	for _, count := range []int{30, 1000, 1000000} {
		h := &history{count: count}
		r := Renderer{}
		calls := 0
		paint := func(row Row) string { calls++; return row.Text }
		viewport := Viewport{Top: count / 2, Rows: 10, Overscan: 2}
		config := Config{Width: 80}
		changes := r.Draw(h, viewport, config, paint)
		if len(changes) != 10 || calls > 14 || h.reads > 14 {
			t.Fatalf("history=%d changes=%d calls=%d reads=%d", count, len(changes), calls, h.reads)
		}
		calls = 0
		h.reads = 0
		if repeat := r.Draw(h, viewport, config, paint); len(repeat) != 0 || calls != 0 || h.reads > 14 {
			t.Fatal("unchanged frame did work or wrote rows")
		}
		config.Theme = "alternate"
		r.Draw(h, viewport, config, paint)
		if calls != min(14, count-viewport.Top+2) {
			t.Fatalf("theme did not invalidate visible cache: %d", calls)
		}
		empty := r.Draw(h, Viewport{}, config, paint)
		if len(empty) != 10 {
			t.Fatal("shrinking frame left old rows")
		}
	}
}

func TestClosedBlocksAndOpenFence(t *testing.T) {
	s := NewStream("item", Config{Width: 80}, nil)
	s.Append(1, "paragraph\n\n```go\ncode\n\n")
	if !s.At(0).Done || s.At(2).Done || s.At(3).Done {
		t.Fatal("open fence frozen or paragraph not frozen")
	}
	s.Append(2, "```\nopen tail")
	if !s.At(2).Done || !s.At(3).Done || s.At(s.Len()-1).Done {
		t.Fatal("closed fence or open tail misclassified")
	}
	r := Renderer{}
	calls := 0
	r.Draw(s, Viewport{Rows: s.Len()}, Config{Width: 80}, func(row Row) string {
		calls++
		if !row.Done {
			t.Fatal("styled open tail")
		}
		return row.Text
	})
	if calls != s.Len()-1 {
		t.Fatalf("closed rows painted %d", calls)
	}
}

func TestResizeWindowDoesNotScanHistory(t *testing.T) {
	s := NewStream("item", Config{Width: 80}, nil)
	prefix := strings.Repeat("history\n", 100000)
	s.Append(1, prefix+strings.Repeat("한글 text ", 100000))
	m := &countingMeasurer{Unicode: Unicode{WidthMode: Graphemes}}
	rows := s.Window(len(prefix), 12, Config{Width: 40, Mode: Graphemes}, m)
	if len(rows) != 12 || m.calls > 600 {
		t.Fatalf("window reflow touched history: rows=%d calls=%d", len(rows), m.calls)
	}
	for _, row := range rows {
		if m.Unicode.Width(row.Text) > 40 {
			t.Fatal("window overflow")
		}
	}
}

func TestFrozenPrefixPaintedOnceAndSealReplaces(t *testing.T) {
	s := NewStream("item", Config{Width: 20}, nil)
	s.Append(1, strings.Repeat("closed paragraph\n\n", 1000)+"open")
	r := Renderer{}
	calls := 0
	paint := func(row Row) string { calls++; return row.Text }
	view := Viewport{Rows: 12, Overscan: 2}
	config := Config{Width: 20}
	cells := make([]string, 12)
	for _, change := range r.Draw(s, view, config, paint) {
		cells[change.Row] = change.Text
	}
	if calls != 14 {
		t.Fatalf("initial visible paints: %d", calls)
	}
	calls = 0
	s.Append(2, " tail")
	if changes := r.Draw(s, view, config, paint); len(changes) != 0 || calls != 0 {
		t.Fatal("append repainted frozen prefix")
	}
	s.Seal(3, "replacement\n")
	changes := r.Draw(s, view, config, paint)
	for _, change := range changes {
		cells[change.Row] = change.Text
	}
	if calls != 2 || cells[0] != "replacement" {
		t.Fatal("seal did not replace visible item")
	}
	for _, cell := range cells[1:] {
		if cell != "" {
			t.Fatal("seal left stale cells")
		}
	}
	if changes = r.Draw(s, view, config, paint); len(changes) != 0 || calls != 2 {
		t.Fatal("seal repainted twice")
	}
}

func TestSanitizeThenClipThenStyle(t *testing.T) {
	rows := Rows{{ID: "item", Revision: 1, Done: true, Text: "\x1b]52;c;hidden\a한글\u202eX"}}
	r := Renderer{}
	changes := r.Draw(rows, Viewport{Rows: 1}, Config{Width: 3}, func(row Row) string {
		if row.Text != "한" {
			t.Fatalf("style received unsafe or unclipped text: %q", row.Text)
		}
		return row.Text
	})
	if len(changes) != 1 || changes[0].Text != "한" {
		t.Fatal("wrong cell patch")
	}
}

func TestResizedWindowKeepsFrozenCache(t *testing.T) {
	s := NewStream("item", Config{Width: 80}, nil)
	s.Append(1, strings.Repeat("closed paragraph\n\n", 20)+"open")
	config := Config{Width: 30}
	r := Renderer{}
	calls := 0
	paint := func(row Row) string { calls++; return row.Text }
	r.Draw(Rows(s.Window(0, 10, config, nil)), Viewport{Rows: 10}, config, paint)
	calls = 0
	s.Append(2, " tail")
	r.Draw(Rows(s.Window(0, 10, config, nil)), Viewport{Rows: 10}, config, paint)
	if calls != 0 {
		t.Fatal("resize window repainted frozen prefix after delta")
	}
}
