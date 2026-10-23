// SPDX-License-Identifier: Apache-2.0

// Package term owns terminal-library adapters and the headless spike harness.
package term

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"io"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Cell is a parsed cell, including the zero-width continuation of a wide cell.
type Cell struct {
	Content    string
	Width      int
	Foreground color.Color
	Bold       bool
}

// Snapshot is an independent copy of the screen after an output write.
// Coordinates are zero-based; Bytes and Writes are cumulative.
type Snapshot struct {
	Cells                   [][]Cell
	CursorX, CursorY        int
	CursorHidden, AltScreen bool
	Bytes, Writes           int
}

// Recorder serializes writes and parses them with a VT emulator. It retains raw
// bytes for measurement, not golden-file comparison. Use only synthetic input:
// a recording is unbounded and can contain all displayed text.
type Recorder struct {
	mu           sync.Mutex
	emulator     *vt.Emulator
	raw          bytes.Buffer
	writes       int
	changed      chan struct{}
	closed       bool
	cursorHidden bool
}

func NewRecorder(width, height int) (*Recorder, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("recording dimensions must be positive")
	}
	emulator := vt.NewEmulator(width, height)
	// This is an output recorder, not a terminal-input peer. Close the response
	// pipe so capability queries cannot block Write awaiting an absent reader.
	// Probe/reply behavior belongs to the real-terminal S5 scenario.
	if err := emulator.InputPipe().(io.Closer).Close(); err != nil {
		return nil, err
	}
	r := &Recorder{emulator: emulator, changed: make(chan struct{})}
	// DECTCEM is global terminal state. x/vt v0.1.0's CursorHidden reads
	// a per-screen cursor copy, which can resurrect the main screen's old
	// visibility on an alternate-screen exit. Track the parsed mode changes
	// rather than altering output or inferring visibility from screen switches.
	emulator.SetCallbacks(vt.Callbacks{
		EnableMode: func(mode ansi.Mode) {
			if mode == ansi.ModeTextCursorEnable {
				r.cursorHidden = false
			}
		},
		DisableMode: func(mode ansi.Mode) {
			if mode == ansi.ModeTextCursorEnable {
				r.cursorHidden = true
			}
		},
	})
	return r, nil
}

func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	n, err := r.emulator.Write(p)
	r.raw.Write(p[:n])
	r.writes++
	close(r.changed)
	r.changed = make(chan struct{})
	return n, err
}

func (r *Recorder) snapshot() Snapshot {
	cursor := r.emulator.CursorPosition()
	s := Snapshot{CursorX: cursor.X, CursorY: cursor.Y, CursorHidden: r.cursorHidden, AltScreen: r.emulator.IsAltScreen(), Bytes: r.raw.Len(), Writes: r.writes}
	s.Cells = make([][]Cell, r.emulator.Height())
	for y := range s.Cells {
		s.Cells[y] = make([]Cell, r.emulator.Width())
		for x := range s.Cells[y] {
			if c := r.emulator.CellAt(x, y); c != nil {
				s.Cells[y][x] = Cell{Content: c.Content, Width: c.Width, Foreground: c.Style.Fg, Bold: c.Style.Attrs&uv.AttrBold != 0}
			}
		}
	}
	return s
}

func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot()
}

// Resize preserves the emulator's existing cells, as a terminal resize does;
// the application must redraw anything invalidated by the new dimensions.
func (r *Recorder) Resize(width, height int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return io.ErrClosedPipe
	}
	if width <= 0 || height <= 0 {
		return errors.New("recording dimensions must be positive")
	}
	r.emulator.Resize(width, height)
	close(r.changed)
	r.changed = make(chan struct{})
	return nil
}

func (r *Recorder) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Clone(r.raw.Bytes())
}

// Wait observes the current screen and subsequent completed writes. The
// predicate runs outside the lock on a copy. A deadline bounds failure only;
// neither elapsed time nor a renderer ticker establishes success.
func (r *Recorder) Wait(ctx context.Context, predicate func(Snapshot) bool) (Snapshot, error) {
	for {
		r.mu.Lock()
		s, changed, closed := r.snapshot(), r.changed, r.closed
		r.mu.Unlock()
		if predicate(s) {
			return s, nil
		}
		if closed {
			return s, io.EOF
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-changed:
		}
	}
}

// Close wakes all waiters; the final screen and raw bytes remain inspectable.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	close(r.changed)
	return r.emulator.Close()
}
