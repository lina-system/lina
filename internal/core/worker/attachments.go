// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MaxMessageFiles     = 10
	MaxMessageFileBytes = 256 << 10
	MaxMessageFileTotal = 512 << 10
)

type MessageFile struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// ReadMessageFiles opens only visible regular text under the assigned worktree.
// OpenRoot confines path resolution; SameFile checks refuse symlink swaps inside
// that root as well, including aliases to hidden configuration directories.
func ReadMessageFiles(workdir string, paths []string) ([]MessageFile, error) {
	if len(paths) > MaxMessageFiles {
		return nil, errors.New("too many message files")
	}
	if len(paths) == 0 {
		return nil, nil
	}
	rootPath, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	inside := func(root, p string) bool {
		rel, err := filepath.Rel(root, p)
		return err == nil && (rel == "." || filepath.IsLocal(rel))
	}
	if inside(rootPath, home) {
		return nil, errors.New("message files require a worktree, not a home directory")
	}
	for _, name := range []string{".config", ".ssh", ".aws", ".gnupg", "Library", "AppData"} {
		if inside(filepath.Join(home, name), rootPath) {
			return nil, errors.New("message files cannot come from home configuration")
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := make([]MessageFile, 0, len(paths))
	total := 0
	for _, path := range paths {
		if !filepath.IsLocal(path) || strings.Contains(path, `\`) {
			return nil, errors.New("message file must be relative to its worktree")
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		for _, part := range parts {
			if part == "" || strings.HasPrefix(part, ".") {
				return nil, errors.New("hidden or traversing message path")
			}
		}
		data, err := readMessageFile(root, parts)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > MaxMessageFileTotal {
			return nil, errors.New("message files exceed total size limit")
		}
		files = append(files, MessageFile{Name: path, Text: string(data)})
	}
	return files, nil
}

func readMessageFile(root *os.Root, parts []string) ([]byte, error) {
	name := parts[0]
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("message file symlinks are refused")
	}
	if len(parts) > 1 {
		if !before.IsDir() {
			return nil, errors.New("invalid message directory")
		}
		dir, err := root.OpenRoot(name)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		after, err := dir.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			return nil, errors.New("message directory changed during read")
		}
		return readMessageFile(dir, parts[1:])
	}
	if !before.Mode().IsRegular() || before.Size() > MaxMessageFileBytes {
		return nil, errors.New("message file must be small regular text")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("message file changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxMessageFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxMessageFileBytes || !utf8.Valid(data) {
		return nil, errors.New("message file must be bounded UTF-8 text")
	}
	for _, b := range data {
		if b < 32 && b != '\n' && b != '\r' && b != '\t' || b == 127 {
			return nil, errors.New("binary message file refused")
		}
	}
	return data, nil
}
