// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const maxRemovalLine = 4 << 20

type journalLine struct {
	Sequence int    `json:"sequence"`
	Previous string `json:"previous"`
	Entry    entry  `json:"entry"`
	Digest   string `json:"digest"`
}

type journal struct {
	file     *os.File
	sync     func() error
	sequence int
	digest   string
	failed   bool
}

func openJournal(path string) (_ *journal, err error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir("backups", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := root.Lstat("backups")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrJournal
	}
	dir, err := root.Open("backups")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if info, err := root.Lstat("backups/removals"); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, ErrJournal
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := root.OpenFile("backups/removals", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	// Persist the new directory and file names as well as their bytes.
	stateDir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	err = errors.Join(f.Sync(), dir.Sync(), stateDir.Sync(), stateDir.Close())
	if err != nil {
		return nil, err
	}
	j := &journal{file: f, sync: f.Sync}
	if _, err := j.read(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *journal) Close() error { return j.file.Close() }

func lineDigest(l journalLine) string {
	l.Digest = ""
	data, _ := json.Marshal(l)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (j *journal) read() ([]entry, error) {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(j.file)
	var entries []entry
	previous := ""
	seen := map[string]bool{}
	for {
		var data []byte
		for {
			fragment, err := reader.ReadSlice('\n')
			data = append(data, fragment...)
			if len(data) > maxRemovalLine {
				return nil, ErrJournal
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				if len(data) != 0 {
					return nil, ErrJournal
				}
				j.sequence, j.digest = len(entries), previous
				return entries, nil
			}
			if err != nil {
				return nil, err
			}
			break
		}
		var line journalLine
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&line); err != nil {
			return nil, errors.Join(ErrJournal, err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, ErrJournal
		}
		if line.Sequence != len(entries)+1 || line.Previous != previous || line.Digest != lineDigest(line) || !line.Entry.valid() || seen[line.Entry.ID] {
			return nil, ErrJournal
		}
		seen[line.Entry.ID] = true
		entries = append(entries, line.Entry)
		previous = line.Digest
	}
}

func (j *journal) append(e entry) error {
	if j.failed {
		return ErrRecovery
	}
	if !e.valid() {
		return ErrInvalid
	}
	line := journalLine{Sequence: j.sequence + 1, Previous: j.digest, Entry: e}
	line.Digest = lineDigest(line)
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxRemovalLine {
		return ErrInvalid
	}
	n, err := j.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.sync()
	}
	if err != nil {
		j.failed = true
		return err
	}
	j.sequence, j.digest = line.Sequence, line.Digest
	return nil
}
