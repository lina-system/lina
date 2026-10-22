// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"slices"
	"strings"
	"testing"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func replyPageReady(rows, previous, lines []string, input string) bool {
	if slices.Equal(rows, previous) || !replyRowsPainted(rows, lines, input) {
		return false
	}
	for overlap := min(len(rows), len(previous)); overlap > 0; overlap-- {
		if slices.Equal(rows[len(rows)-overlap:], previous[:overlap]) {
			return true
		}
	}
	return false
}

// PTY reads can split a redraw between any two cells. The fixture's complete,
// consecutive lines establish a painted viewport without relying on timing or
// assuming that a completed Recorder write is a complete renderer frame.
func replyRowsPainted(rows, lines []string, input string) bool {
	firstLine, firstRow, lastLine, lastRow := -1, -1, -1, -1
	for row, text := range rows {
		for line, want := range lines {
			if !strings.Contains(text, want) {
				continue
			}
			if lastLine >= 0 && (line != lastLine+1 || row != lastRow+1) {
				return false
			}
			if firstLine < 0 {
				firstLine, firstRow = line, row
			}
			lastLine, lastRow = line, row
			break
		}
	}
	if firstLine < 0 {
		return false
	}
	if firstLine == 0 {
		if !strings.Contains(strings.Join(rows[:firstRow], "\n"), input) {
			return false
		}
	} else if firstRow != 0 {
		return false
	}
	// Only the final reply line can be followed by activity/pager metadata.
	return lastLine == len(lines)-1 || lastRow == len(rows)-1
}

func TestReplyShapeScrollRejectsPartialPaint(t *testing.T) {
	lines := []string{"reply 1", "reply 2", "reply 3", "reply 4", "reply 5", "reply 6"}
	r, err := tuiterm.NewRecorder(20, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	write := func(raw string) []string {
		t.Helper()
		if _, err := r.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		return screenRows(r.Snapshot())
	}
	previous := write("\x1b[Hreply 4\r\nreply 5\r\nreply 6\r\npager")
	if replyPageReady(previous, previous, lines, "request") {
		t.Fatal("unchanged viewport accepted as a scroll")
	}
	// A PTY read can stop after only the first two rows of the repaint.
	partial := write("\x1b[Hreply 2\x1b[K\r\nreply 3\x1b[K")
	if replyPageReady(partial, previous, lines, "request") {
		t.Fatal("partially painted viewport accepted as the next page")
	}
	complete := write("\x1b[3;1Hreply 4\x1b[K\r\nreply 5\x1b[K")
	if !replyPageReady(complete, previous, lines, "request") {
		t.Fatal("complete overlapping viewport not accepted")
	}
	// A fully painted but broken page must still fail the original coverage
	// requirement; waiting for a frame must not hide an actual skipped row.
	skipped := write("\x1b[Hreply 2\x1b[K\r\nreply 4\x1b[K\r\nreply 5\x1b[K\r\nreply 6\x1b[K")
	if replyPageReady(skipped, previous, lines, "request") {
		t.Fatal("page with a missing reply row accepted")
	}
	if replyPageReady([]string{"reply 2", "reply 3"}, []string{"reply 5", "reply 6"}, lines, "request") {
		t.Fatal("page without overlap accepted")
	}
	first := []string{"request", "", "reply 1", "reply 2"}
	if !replyPageReady(first, complete, lines, "request") {
		t.Fatal("complete first page not accepted")
	}
	first[0] = ""
	if replyPageReady(first, complete, lines, "request") {
		t.Fatal("first page accepted before its input was painted")
	}
}
