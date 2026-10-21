// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"bytes"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

var ErrPatchContext = errors.New("stale_patch_context")

// FileChange is a planned replacement or recoverable deletion. Planning is
// pure; the executor checks revisions again before writing any bytes.
type FileChange struct {
	Path    string
	Content []byte
	Delete  bool
}

type patchHunk struct {
	after    string
	old, new []string
	eof      bool
}
type patchEdit struct {
	path, move, kind string
	content          []byte
	hunks            []patchHunk
}

// FilePath accepts relative, canonical paths and excludes Git's control files.
// The filesystem boundary must additionally refuse symlinks and special files.
func FilePath(name string) bool {
	if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return utf8.ValidString(name)
}

func parsePatch(text string) ([]patchEdit, error) {
	if !utf8.ValidString(text) || len(text) > 8<<20 {
		return nil, ErrInvalid
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 || lines[0] != "*** Begin Patch" || lines[len(lines)-1] != "*** End Patch" {
		return nil, ErrInvalid
	}
	lines = lines[1 : len(lines)-1]
	var edits []patchEdit
	seen := map[string]bool{}
	claim := func(name string) bool {
		if !FilePath(name) || seen[name] {
			return false
		}
		seen[name] = true
		return true
	}
	for len(lines) > 0 {
		var e patchEdit
		for _, kind := range []string{"Add", "Update", "Delete"} {
			if name, ok := strings.CutPrefix(lines[0], "*** "+kind+" File: "); ok {
				e.kind, e.path = kind, name
				break
			}
		}
		if e.kind == "" || !claim(e.path) {
			return nil, ErrInvalid
		}
		lines = lines[1:]
		switch e.kind {
		case "Add":
			for len(lines) > 0 && strings.HasPrefix(lines[0], "+") {
				e.content = append(e.content, lines[0][1:]...)
				e.content = append(e.content, '\n')
				lines = lines[1:]
			}
		case "Update":
			if len(lines) > 0 && strings.HasPrefix(lines[0], "*** Move to: ") {
				e.move = strings.TrimPrefix(lines[0], "*** Move to: ")
				if !claim(e.move) {
					return nil, ErrInvalid
				}
				lines = lines[1:]
			}
			for len(lines) > 0 && (lines[0] == "@@" || strings.HasPrefix(lines[0], "@@ ")) {
				h := patchHunk{}
				if lines[0] != "@@" {
					h.after = strings.TrimPrefix(lines[0], "@@ ")
				}
				lines = lines[1:]
				count := 0
				for len(lines) > 0 && len(lines[0]) > 0 && strings.ContainsRune(" +-", rune(lines[0][0])) {
					line := lines[0]
					if line[0] != '+' {
						h.old = append(h.old, line[1:])
					}
					if line[0] != '-' {
						h.new = append(h.new, line[1:])
					}
					count++
					lines = lines[1:]
				}
				if count == 0 {
					return nil, ErrInvalid
				}
				if len(lines) > 0 && lines[0] == "*** End of File" {
					h.eof = true
					lines = lines[1:]
				}
				e.hunks = append(e.hunks, h)
				if h.eof {
					break
				}
			}
			if len(e.hunks) == 0 {
				return nil, ErrInvalid
			}
		case "Delete":
		}
		edits = append(edits, e)
	}
	return edits, nil
}

// PatchPaths validates grammar and returns every source and destination that
// must be read and revision checked, in deterministic order.
func PatchPaths(text string) ([]string, error) {
	edits, err := parsePatch(text)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range edits {
		paths = append(paths, e.path)
		if e.move != "" {
			paths = append(paths, e.move)
		}
	}
	return paths, nil
}

// PlanPatch accepts Codex's *** Begin Patch grammar. Missing map entries mean
// absent files. Every hunk must match exactly, including whitespace and EOF;
// failure returns no changes and never mutates the supplied snapshots.
func PlanPatch(text string, files map[string][]byte) ([]FileChange, error) {
	edits, err := parsePatch(text)
	if err != nil {
		return nil, err
	}
	var changes []FileChange
	for _, e := range edits {
		data, exists := files[e.path]
		switch e.kind {
		case "Add":
			if exists {
				return nil, ErrPatchContext
			}
			changes = append(changes, FileChange{Path: e.path, Content: e.content})
		case "Delete":
			if !exists {
				return nil, ErrPatchContext
			}
			changes = append(changes, FileChange{Path: e.path, Delete: true})
		case "Update":
			if !exists || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				return nil, ErrPatchContext
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(data) == 0 {
				lines = nil
			}
			var output []string
			cursor := 0
			for _, h := range e.hunks {
				start := cursor
				if h.after != "" {
					pos := slices.Index(lines[start:], h.after)
					if pos < 0 {
						return nil, ErrPatchContext
					}
					start += pos + 1
				}
				found := -1
				for i := start; i+len(h.old) <= len(lines); i++ {
					if h.eof && i+len(h.old) != len(lines) {
						continue
					}
					if slices.Equal(lines[i:i+len(h.old)], h.old) {
						found = i
						break
					}
				}
				if found < 0 {
					return nil, ErrPatchContext
				}
				output = append(output, lines[cursor:found]...)
				output = append(output, h.new...)
				cursor = found + len(h.old)
			}
			output = append(output, lines[cursor:]...)
			content := []byte(strings.Join(output, "\n"))
			if len(output) > 0 {
				content = append(content, '\n')
			}
			name := e.path
			if e.move != "" {
				if _, exists := files[e.move]; exists {
					return nil, ErrPatchContext
				}
				name = e.move
			}
			changes = append(changes, FileChange{Path: name, Content: content})
			if e.move != "" {
				changes = append(changes, FileChange{Path: e.path, Delete: true})
			}
		}
	}
	return changes, nil
}
