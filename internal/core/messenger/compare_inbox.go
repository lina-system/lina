// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrComparisonInbox = errors.New("comparison_inbox_refused")

const comparisonLineLimit = 8 << 20

type inboxFingerprint struct {
	info os.FileInfo
	sum  [32]byte
}

// The inbox is the collector's immediate <chat>.jsonl files, never its state
// file, config, spool or database. All errors omit content and filesystem paths.
func readComparisonInbox(ctx context.Context, path string, req ComparisonRequest) (comparisonIDs, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrComparisonInbox
	}
	defer root.Close()
	names, err := comparisonFiles(root)
	if err != nil {
		return nil, err
	}
	ids := comparisonIDs{}
	fingerprints := map[string]inboxFingerprint{}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, info, err := openComparisonFile(root, name)
		if err != nil {
			return nil, err
		}
		chat := strings.TrimSuffix(name, ".jsonl")
		ids.chat(chat)
		hash := sha256.New()
		reader := bufio.NewReaderSize(io.TeeReader(io.LimitReader(file, info.Size()+1), hash), 64<<10)
		line := 0
		for {
			raw, readErr := readComparisonLine(reader)
			if errors.Is(readErr, io.EOF) && len(raw) == 0 {
				break
			}
			line++
			if readErr != nil {
				file.Close()
				return nil, fmt.Errorf("%w: line %d: incomplete or oversized record", ErrComparisonInbox, line)
			}
			id, at, err := comparisonRecord(raw, chat)
			if err != nil {
				file.Close()
				return nil, fmt.Errorf("%w: line %d: invalid record", ErrComparisonInbox, line)
			}
			if !at.Before(req.Since) && at.Before(req.Until) {
				ids[chat][id] = true
			}
			if err := ctx.Err(); err != nil {
				file.Close()
				return nil, err
			}
		}
		after, err := file.Stat()
		file.Close()
		if err != nil || !sameComparisonFile(info, after) {
			return nil, ErrComparisonInbox
		}
		fingerprints[name] = inboxFingerprint{info: info, sum: [32]byte(hash.Sum(nil))}
	}
	// A second bounded pass detects replacement or edits even when byte count
	// and mtime did not change. Never publish a partial comparison.
	for _, name := range names {
		file, info, err := openComparisonFile(root, name)
		if err != nil {
			return nil, err
		}
		expected := fingerprints[name]
		hash := sha256.New()
		_, err = io.Copy(hash, &comparisonContextReader{ctx: ctx, r: io.LimitReader(file, expected.info.Size()+1)})
		after, statErr := file.Stat()
		file.Close()
		if err != nil {
			return nil, err
		}
		if statErr != nil || !sameComparisonFile(expected.info, info) || !sameComparisonFile(info, after) || [32]byte(hash.Sum(nil)) != expected.sum {
			return nil, ErrComparisonInbox
		}
	}
	after, err := comparisonFiles(root)
	if err != nil || !slices.Equal(names, after) {
		return nil, ErrComparisonInbox
	}
	return ids, nil
}

type comparisonContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *comparisonContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func comparisonFiles(root *os.Root) ([]string, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, ErrComparisonInbox
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, ErrComparisonInbox
	}
	names := []string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if !comparisonDigits(strings.TrimSuffix(entry.Name(), ".jsonl"), 20) || !entry.Type().IsRegular() {
			return nil, ErrComparisonInbox
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, ErrComparisonInbox
	}
	slices.Sort(names)
	return names, nil
}
func openComparisonFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, ErrComparisonInbox
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, ErrComparisonInbox
	}
	opened, err := file.Stat()
	if err != nil || !sameComparisonFile(info, opened) {
		file.Close()
		return nil, nil, ErrComparisonInbox
	}
	return file, opened, nil
}
func sameComparisonFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func comparisonDigits(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func readComparisonLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > comparisonLineLimit {
			return nil, ErrComparisonInbox
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
func comparisonRecord(raw []byte, chat string) (string, time.Time, error) {
	var row struct {
		Type      string          `json:"type"`
		Source    string          `json:"source"`
		ID        json.RawMessage `json:"message_id"`
		Timestamp json.RawMessage `json:"timestamp"`
	}
	if !json.Valid(raw) || json.Unmarshal(raw, &row) != nil || row.Type != "message" || (row.Source != "kakao" && row.Source != "agent-messenger") {
		return "", time.Time{}, ErrComparisonInbox
	}
	var id string
	if json.Unmarshal(row.ID, &id) != nil {
		id = string(row.ID)
	}
	id = strings.TrimPrefix(id, chat+"-")
	digits := strings.TrimPrefix(id, "-")
	if !comparisonDigits(digits, 19) {
		return "", time.Time{}, ErrComparisonInbox
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return "", time.Time{}, ErrComparisonInbox
	}
	var stamp string
	var at time.Time
	var err error
	if json.Unmarshal(row.Timestamp, &stamp) == nil {
		at, err = time.Parse(time.RFC3339Nano, stamp)
	} else {
		// Older collector envelopes contain Unix seconds; never decode IDs or
		// timestamps through float64.
		var seconds int64
		decoder := json.NewDecoder(bytes.NewReader(row.Timestamp))
		err = decoder.Decode(&seconds)
		if bytes.Equal(row.Timestamp, []byte("null")) {
			err = ErrComparisonInbox
		}
		at = time.Unix(seconds, 0)
	}
	if err != nil || at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return "", time.Time{}, ErrComparisonInbox
	}
	return id, at, nil
}
