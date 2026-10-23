// SPDX-License-Identifier: Apache-2.0
package doing

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
)

const readLimit int64 = 2 << 20

// Tail belongs to one session/source; no process-global cache retains sessions.
// JSONL reads only appended bytes, including a bounded pending partial line.
// Rewritten JSON documents (Gemini) are read only when their metadata changes.
type Tail struct {
	info            os.FileInfo
	offset          int64
	pending         []byte
	whole, skipping bool
}

func (t *Tail) Read(path, kind string, r *Reader, at time.Time) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	same := t.info != nil && os.SameFile(t.info, info)
	if same && t.offset == info.Size() && info.ModTime() == t.info.ModTime() {
		return 0, nil
	}
	reset := t.info != nil && (!same || info.Size() < t.offset || t.whole || info.Size() == t.offset)
	if reset {
		t.offset = 0
		t.pending = nil
		t.skipping = false
		r.title = ""
		r.step = ""
	}
	start := t.offset
	if start == 0 && info.Size() > readLimit {
		start = info.Size() - readLimit
		t.skipping = true
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return 0, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, readLimit))
	if err != nil {
		return 0, err
	}
	t.info = info
	t.offset = start + int64(len(buf))
	if kind == "gemini" && start == 0 && json.Valid(buf) {
		t.whole = true
		t.pending = nil
		r.Record(kind, buf, at)
		return int64(len(buf)), nil
	}
	t.whole = false
	data := append(t.pending, buf...)
	t.pending = nil
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		if !t.skipping {
			r.Record(kind, data[:end], at)
		}
		t.skipping = false
		data = data[end+1:]
	}
	if len(data) <= int(readLimit) {
		t.pending = append([]byte(nil), data...)
	} else {
		t.skipping = true
	}
	return int64(len(buf)), nil
}
