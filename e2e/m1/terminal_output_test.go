// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

func copyTerminalScreen(screen *tuiterm.Recorder, input io.Reader) error {
	writer := &terminalScreenWriter{screen: screen}
	_, err := io.Copy(writer, input)
	// Linux ends a PTY with EIO; cleanup can also close the master. Neither
	// makes a partial renderer write into a completed screen.
	if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
		err = nil
	}
	if len(writer.pending) != 0 {
		return errors.Join(err, io.ErrUnexpectedEOF)
	}
	return err
}

// rawTerminalWriter ends every renderer write by restoring full-height
// scrolling margins and the cursor. Reassemble those writes before publishing
// cells: the PTY can split or combine them at any byte, including inside ANSI
// sequences and UTF-8. No product hook, sleep, or quiet-time guess is needed.
var terminalWriteEnd = []byte(ansi.SaveCursor + ansi.SetTopBottomMargins(0, 0) + ansi.RestoreCursor)

type terminalScreenWriter struct {
	screen  *tuiterm.Recorder
	tail    []byte
	pending []byte
	framed  bool
}

func (w *terminalScreenWriter) Write(data []byte) (int, error) {
	n := len(data)
	if !w.framed {
		// Connection-only and NO_COLOR output have no renderer. Forward them
		// normally until its first trailer, retaining only a search suffix.
		probe := append(bytes.Clone(w.tail), data...)
		at := bytes.Index(probe, terminalWriteEnd)
		if at < 0 {
			w.tail = bytes.Clone(probe[max(0, len(probe)-len(terminalWriteEnd)+1):])
			return w.screen.Write(data)
		}
		end := at + len(terminalWriteEnd) - len(w.tail)
		if _, err := w.screen.Write(data[:end]); err != nil {
			return 0, err
		}
		w.framed, w.tail = true, nil
		data = data[end:]
	}
	w.pending = append(w.pending, data...)
	for {
		at := bytes.Index(w.pending, terminalWriteEnd)
		if at < 0 {
			return n, nil
		}
		end := at + len(terminalWriteEnd)
		if _, err := w.screen.Write(w.pending[:end]); err != nil {
			return 0, err
		}
		w.pending = w.pending[end:]
	}
}

// A PTY is a byte stream, not a renderer-write stream. Hold the rest of a
// redraw until the recorder has consumed its prefix, at every possible split.
// No scheduling, concurrent tests, or elapsed-time assertion is involved.
func TestTerminalScreenReadBoundaries(t *testing.T) {
	trailer := ansi.SaveCursor + ansi.SetTopBottomMargins(0, 0) + ansi.RestoreCursor
	initial := "\x1b[?1049h\x1b[2J\x1b[HOLD-1\r\nOLD-2\r\nOLD-3\x1b[4;3H\x1b[?25h" + trailer
	redraw := "\x1b[HNEW-1\r\nNEW-2\r\n한글-3\x1b[4;3H" + trailer
	for split := 1; split < len(redraw); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			screen, err := tuiterm.NewRecorder(20, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer screen.Close()
			var before tuiterm.Snapshot
			input := &screenChunks{chunks: []string{initial, redraw[:split], redraw[split:]}, before: func(chunk int) {
				switch chunk {
				case 1:
					before = screen.Snapshot()
				case 2:
					if got := screen.Snapshot(); !reflect.DeepEqual(got.Cells, before.Cells) || got.CursorX != before.CursorX || got.CursorY != before.CursorY {
						t.Errorf("partial renderer write became an observable screen at byte %d:\n%s", split, screenText(got))
					}
				}
			}}
			if err := copyTerminalScreen(screen, input); err != nil {
				t.Fatal(err)
			}
			complete, _ := tuiterm.NewRecorder(20, 4)
			defer complete.Close()
			if _, err := complete.Write([]byte(initial + redraw)); err != nil {
				t.Fatal(err)
			}
			got, want := screen.Snapshot(), complete.Snapshot()
			if !reflect.DeepEqual(got.Cells, want.Cells) || got.CursorX != want.CursorX || got.CursorY != want.CursorY || !got.AltScreen || got.CursorHidden {
				t.Fatalf("completed redraw changed with read boundaries:\n%s", screenText(got))
			}
			if !bytes.Equal(screen.Bytes(), []byte(initial+redraw)) {
				t.Fatal("recording lost or duplicated output bytes")
			}
		})
	}
}

func TestTerminalScreenCoalescedFrames(t *testing.T) {
	trailer := ansi.SaveCursor + ansi.SetTopBottomMargins(0, 0) + ansi.RestoreCursor
	initial := "\x1b[HOLD" + trailer
	for _, split := range []int{1, len(trailer) - 1, len(trailer)} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			screen, _ := tuiterm.NewRecorder(10, 2)
			defer screen.Close()
			// The first trailer spans reads; the next redraw starts in the same
			// read that finishes it. Neither read size is a frame boundary.
			input := &screenChunks{chunks: []string{initial[:len(initial)-split], initial[len(initial)-split:] + "\x1b[HNEW", trailer}, before: func(chunk int) {
				if chunk == 2 && !strings.HasPrefix(screenText(screen.Snapshot()), "OLD") {
					t.Error("coalesced partial redraw replaced the completed screen")
				}
			}}
			if err := copyTerminalScreen(screen, input); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(screenText(screen.Snapshot()), "NEW") {
				t.Fatal("completed redraw was not published")
			}
		})
	}
}

func TestTerminalScreenPlainAndTruncatedOutput(t *testing.T) {
	trailer := ansi.SaveCursor + ansi.SetTopBottomMargins(0, 0) + ansi.RestoreCursor
	for _, tc := range []struct {
		name, output string
		end, want    error
	}{
		{"plain", "plain transcript\r\n", io.EOF, nil},
		{"complete", "\x1b[Hcomplete" + trailer, syscall.EIO, nil},
		{"truncated", "\x1b[Hcomplete" + trailer + "\x1b[Hpartial", io.EOF, io.ErrUnexpectedEOF},
		{"truncated-pty", "\x1b[Hcomplete" + trailer + "\x1b[Hpartial", syscall.EIO, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen, _ := tuiterm.NewRecorder(30, 3)
			defer screen.Close()
			err := copyTerminalScreen(screen, io.MultiReader(strings.NewReader(tc.output), screenReadError{tc.end}))
			if !errors.Is(err, tc.want) {
				t.Fatalf("stream termination: got %v, want %v", err, tc.want)
			}
			if tc.want == nil && string(screen.Bytes()) != tc.output {
				t.Fatal("plain or complete output bytes changed")
			}
			if tc.want != nil && strings.Contains(screenText(screen.Snapshot()), "partial") {
				t.Fatal("truncated redraw became visible")
			}
		})
	}
}

type screenReadError struct{ err error }

func (r screenReadError) Read([]byte) (int, error) { return 0, r.err }

type screenChunks struct {
	chunks []string
	next   int
	before func(int)
}

func (r *screenChunks) Read(p []byte) (int, error) {
	if r.next == len(r.chunks) {
		return 0, io.EOF
	}
	if r.before != nil {
		r.before(r.next)
	}
	n := copy(p, r.chunks[r.next])
	r.chunks[r.next] = r.chunks[r.next][n:]
	if r.chunks[r.next] == "" {
		r.next++
	}
	return n, nil
}
