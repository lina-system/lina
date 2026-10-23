// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"io"
	"strconv"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// Bubble Tea v2.1 has no post-flush callback. Its window title is emitted in
// the same Write as the corresponding screen diff, even for identical content.
// Use that field as a private frame fence and remove it before terminal output.
// Unlike a tea.Cmd or tea.Raw acknowledgement, this cannot precede the diff.
// Only the connection owner submits frames; at most one receipt is pending.
type frameWriter struct {
	mu      sync.Mutex
	output  io.Writer
	serial  uint64
	marker  string
	pending chan error
	private bool
}

const frameTitlePrefix = "lina-frame:"

func (w *frameWriter) next() (string, <-chan error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.serial++
	title := frameTitlePrefix + strconv.FormatUint(w.serial, 10)
	w.marker = ansi.SetWindowTitle(title)
	w.pending = make(chan error, 1)
	return title, w.pending
}

func (w *frameWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	length := len(data)
	matched := w.pending != nil && bytes.Contains(data, []byte(w.marker))
	// Strip private fences even when Bubble Tea re-emits its last view during
	// shutdown. Its following empty-title reset must not change the real title.
	prefix := []byte("\x1b]2;" + frameTitlePrefix)
	for {
		start := bytes.Index(data, prefix)
		if start < 0 {
			break
		}
		end := bytes.IndexByte(data[start:], '\a')
		if end < 0 {
			break
		}
		data = bytes.Join([][]byte{data[:start], data[start+end+1:]}, nil)
		w.private = true
	}
	if w.private {
		data = bytes.ReplaceAll(data, []byte(ansi.SetWindowTitle("")), nil)
	}
	n, err := w.output.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if w.pending != nil && (matched || err != nil) {
		w.pending <- err
		w.pending = nil
	}
	if err != nil {
		return 0, err
	}
	return length, nil
}
