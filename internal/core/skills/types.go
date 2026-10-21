// SPDX-License-Identifier: Apache-2.0

// Package skills owns the common catalog and immutable Git skill versions.
// Callers supply a fenced canon writer and install Schema in a numbered migration.
package skills

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrInvalid    = errors.New("invalid_skill")
	ErrAcceptance = errors.New("skill_requires_explicit_acceptance")
	ErrConflict   = errors.New("stale_skill_revision")
	ErrNotFound   = errors.New("skill_not_found")
	ErrUndone     = errors.New("skill_content_was_undone")
	ErrCorrupt    = errors.New("skill_version_corrupt")
)

const Schema = `
CREATE TABLE skill_versions (
 id TEXT NOT NULL, revision INTEGER NOT NULL, commit_id TEXT NOT NULL,
 name TEXT NOT NULL, description TEXT NOT NULL, content_hash TEXT NOT NULL,
 deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
 PRIMARY KEY(id,revision)
);
CREATE TABLE skill_heads (id TEXT PRIMARY KEY NOT NULL, revision INTEGER NOT NULL);
CREATE TABLE skill_undone (id TEXT NOT NULL, content_hash TEXT NOT NULL, PRIMARY KEY(id,content_hash));
CREATE TABLE skill_repo (singleton INTEGER PRIMARY KEY CHECK(singleton=1), commit_id TEXT NOT NULL);
`

type Canon interface {
	Read(context.Context, func(*canon.Tx) error) error
	Write(context.Context, func(*canon.Tx) error) error
}

// BuiltinFS supplies a build-time identity covering every embedded file. It
// lets the index issue generation-scoped handles without reading skill bodies.
// The same identity must never designate different bytes.
type BuiltinFS interface {
	fs.FS
	SkillRevision() string
}

// File is text plus its executable mode. The automatic path never accepts code.
type File struct {
	Text       string `json:"text"`
	Executable bool   `json:"executable,omitempty"`
}
type Bundle map[string]File

// Version is metadata only. Holding it pins the exact body for a prepared turn.
type Version struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Revision    int64  `json:"revision"`
	Commit      string `json:"commit"`
}
type Body struct {
	Version Version `json:"version"`
	Files   Bundle  `json:"files"`
}
type Patch struct {
	ID       string `json:"id"`
	Expected int64  `json:"expected"`
	Files    Bundle `json:"files"` // changed files, merged with the complete current bundle
}
type Undo struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}
type Applied struct {
	Version Version `json:"version"`
	Notice  string  `json:"notice"`
	Undo    Undo    `json:"undo"`
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 80 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func line(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) && !strings.ContainsAny(s, "\u2028\u2029")
}

// The reversible format deliberately accepts only plain or quoted one-line
// name/description scalars. Unknown YAML keys, aliases, tags and structures go
// through explicit acceptance; they can never widen automatic-write authority.
func metadata(text string) (name, description string, err error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || !strings.HasPrefix(text, "---\n") {
		return "", "", ErrInvalid
	}
	header, body, ok := strings.Cut(text[4:], "\n---\n")
	if !ok || strings.TrimSpace(body) == "" {
		return "", "", ErrInvalid
	}
	seen := map[string]bool{}
	for _, row := range strings.Split(header, "\n") {
		row = strings.TrimSpace(row)
		if row == "" || strings.HasPrefix(row, "#") {
			continue
		}
		key, value, ok := strings.Cut(row, ":")
		if !ok || seen[key] {
			return "", "", ErrInvalid
		}
		seen[key] = true
		if key != "name" && key != "description" {
			return "", "", ErrAcceptance
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		} else if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "&") || strings.HasPrefix(value, "*") || strings.HasPrefix(value, "!") || value == "|" || value == ">" {
			return "", "", ErrAcceptance
		}
		if !line(value) {
			return "", "", ErrInvalid
		}
		if key == "name" {
			name = value
		} else {
			description = value
		}
	}
	if !validID(name) || !line(description) {
		return "", "", ErrInvalid
	}
	return name, description, nil
}
func instructionOnly(files Bundle) error {
	if len(files) == 0 {
		return ErrInvalid
	}
	for filePath, f := range files {
		if !fs.ValidPath(filePath) || strings.Contains(filePath, "\\") || strings.HasPrefix(filePath, ".") {
			return ErrInvalid
		}
		for parent := path.Dir(filePath); parent != "."; parent = path.Dir(parent) {
			if _, exists := files[parent]; exists {
				return ErrInvalid
			}
		}
		for _, part := range strings.Split(filePath, "/") {
			if strings.HasPrefix(part, ".") {
				return ErrInvalid
			}
		}
		if f.Executable || !strings.HasSuffix(filePath, ".md") || !utf8.ValidString(f.Text) || strings.ContainsRune(f.Text, 0) {
			return ErrAcceptance
		}
	}
	_, _, err := metadata(files["SKILL.md"].Text)
	return err
}
