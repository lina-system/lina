// SPDX-License-Identifier: Apache-2.0
package doing

import (
	"encoding/json"
	"github.com/thisisjun786/lina/kit/proc"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClaudeFiles reads only the bound session under the worker's own config root.
// Transcript paths never come from model text. Missing sources are optional.
type ClaudeFiles struct {
	Root, Session string
	transcript    string
	tail          Tail
	tasks         map[string]taskFile
}
type taskFile struct {
	info os.FileInfo
	raw  json.RawMessage
}

func ClaudeRoot(environment []string) string {
	if environment == nil {
		environment = proc.WorkerEnvironment()
	}
	var home, config string
	for _, s := range environment {
		if v, ok := strings.CutPrefix(s, "HOME="); ok {
			home = v
		}
		if v, ok := strings.CutPrefix(s, "CLAUDE_CONFIG_DIR="); ok {
			config = v
		}
	}
	if config != "" && filepath.IsAbs(config) {
		return config
	}
	if home != "" && filepath.IsAbs(home) {
		return filepath.Join(home, ".claude")
	}
	return ""
}
func (c *ClaudeFiles) Read(r *Reader, at time.Time) {
	if c.Root == "" || c.Session == "" || c.Session == "." || c.Session == ".." || strings.ContainsAny(c.Session, "/\\") {
		return
	}
	if c.transcript == "" {
		dirs, _ := os.ReadDir(filepath.Join(c.Root, "projects"))
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			path := filepath.Join(c.Root, "projects", dir.Name(), c.Session+".jsonl")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				c.transcript = path
				break
			}
		}
	}
	if c.transcript != "" {
		_, _ = c.tail.Read(c.transcript, "claude", r, at)
	}
	dir := filepath.Join(c.Root, "tasks", c.Session)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 256 {
		return
	}
	next := make(map[string]taskFile, len(entries))
	var current json.RawMessage
	var currentID int64 = -1
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > readLimit {
			continue
		}
		cached := c.tasks[entry.Name()]
		if cached.info == nil || !os.SameFile(cached.info, info) || cached.info.ModTime() != info.ModTime() || cached.info.Size() != info.Size() {
			f, err := os.Open(filepath.Join(dir, entry.Name()))
			if err != nil {
				return
			}
			raw, err := io.ReadAll(io.LimitReader(f, readLimit+1))
			f.Close()
			if err != nil || int64(len(raw)) > readLimit || !json.Valid(raw) {
				return
			}
			cached = taskFile{info: info, raw: raw}
		}
		next[entry.Name()] = cached
		v := obj(cached.raw)
		if str(v["status"]) != "in_progress" {
			continue
		}
		id, _ := strconv.ParseInt(str(v["id"]), 10, 64)
		if id > currentID {
			currentID = id
			current = cached.raw
		}
	}
	c.tasks = next
	// An existing task directory is the latest task inventory, including empty.
	if current == nil {
		r.step = ""
	} else {
		r.plan(append(append([]byte{'['}, current...), ']'))
	}
}
