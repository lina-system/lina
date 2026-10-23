// SPDX-License-Identifier: Apache-2.0
package miniapps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	// MaxBytes bounds one revision of app files. The fixed template is served
	// from the build, never counted or writable here.
	MaxBytes      = 256 << 10
	MaxFileBytes  = 64 << 10
	MaxFiles      = 16
	MaxStoreBytes = 64 << 10
)

var appFilePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}\.(html|js|css|svg|json|txt)$`)

// File is one immutable entry of a revision. The bytes live under the app's
// revision directory; only their identity is stored in the database.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
}

func validatePath(name string) error {
	if !appFilePattern.MatchString(name) || path.Base(name) != name || strings.Contains(name, "..") {
		return ErrInvalid
	}
	return nil
}

// ValidateFiles applies the pre-live size and path rules before any browser
// check. The app must ship an index page and its own script; the template
// supplies styles, fonts and the client.
func ValidateFiles(files map[string][]byte) error {
	if len(files) == 0 || len(files) > MaxFiles {
		return ErrInvalid
	}
	if _, ok := files["index.html"]; !ok {
		return ErrInvalid
	}
	if _, ok := files["app.js"]; !ok {
		return ErrInvalid
	}
	var total int64
	for name, data := range files {
		if err := validatePath(name); err != nil {
			return err
		}
		if len(data) > MaxFileBytes {
			return ErrInvalid
		}
		total += int64(len(data))
	}
	if total > MaxBytes || total == 0 {
		return ErrInvalid
	}
	return nil
}

func fileInfos(files map[string][]byte) []File {
	out := make([]File, 0, len(files))
	for name, data := range files {
		sum := sha256.Sum256(data)
		out = append(out, File{Path: name, Size: int64(len(data)), Hash: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// SameFiles compares identity and bytes, never only names or sizes.
func SameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		other, ok := b[name]
		if !ok || string(other) != string(data) {
			return false
		}
	}
	return true
}

// DiffSummary names the files a change touched and its line counts. Equal files
// return changed=false and an empty summary; a no-op records nothing.
func DiffSummary(old, next map[string][]byte) (summary string, changed bool) {
	names := make([]string, 0, len(old)+len(next))
	for name := range old {
		names = append(names, name)
	}
	for name := range next {
		if _, ok := old[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var parts []string
	files := 0
	for _, name := range names {
		before, after := old[name], next[name]
		if string(before) == string(after) {
			continue
		}
		files++
		added, removed := lineDiff(before, after)
		parts = append(parts, fmt.Sprintf("%s +%d −%d", name, added, removed))
	}
	if files == 0 {
		return "", false
	}
	summary = fmt.Sprintf("%d files: %s", files, strings.Join(parts, ", "))
	if len(summary) > 300 {
		summary = fmt.Sprintf("%d files changed", files)
	}
	return summary, true
}

// lineDiff counts lines that only exist on one side. It is a display summary,
// not a patch; identical bytes are already handled by the caller.
func lineDiff(before, after []byte) (added, removed int) {
	count := func(data []byte) map[string]int {
		out := map[string]int{}
		for _, line := range strings.Split(string(data), "\n") {
			out[line]++
		}
		return out
	}
	a, b := count(before), count(after)
	for line, n := range b {
		if extra := n - a[line]; extra > 0 {
			added += extra
		}
	}
	for line, n := range a {
		if extra := n - b[line]; extra > 0 {
			removed += extra
		}
	}
	return added, removed
}

func (s *Service) revisionDir(appID string, revision int64) string {
	return filepath.Join(s.cfg.Root, "miniapps", appID, "r", fmt.Sprintf("%d", revision))
}

// writeRevision stores one revision atomically. A crash leaves either the whole
// directory or none of it.
func (s *Service) writeRevision(appID string, revision int64, files map[string][]byte) error {
	final := s.revisionDir(appID, revision)
	if _, err := os.Stat(final); err == nil {
		return ErrInvalid
	}
	tmp := final + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	for name, data := range files {
		if err := validatePath(name); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
		if err := os.WriteFile(filepath.Join(tmp, name), data, 0o600); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// captureDir keeps the desktop capture the pre-live check made of one revision.
// A capture never renders as an app file, so it cannot be served on the origin.
func (s *Service) captureDir(appID string) string {
	return filepath.Join(s.cfg.Root, "miniapps", appID, "captures")
}

// writeCapture stores the checked render beside the revision. No capture means
// the check did not run a browser, which is not an error here.
func (s *Service) writeCapture(appID string, revision int64, png []byte) error {
	if len(png) == 0 {
		return nil
	}
	dir := s.captureDir(appID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, fmt.Sprintf("%d.png", revision)), png)
}

func (s *Service) readRevision(appID string, revision int64) (map[string][]byte, error) {
	dir := s.revisionDir(appID, revision)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, ErrInvalid
		}
		name := entry.Name()
		if err := validatePath(name); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if len(data) > MaxFileBytes {
			return nil, ErrInvalid
		}
		files[name] = data
	}
	if err := ValidateFiles(files); err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Service) removeAppDir(appID string) error {
	return os.RemoveAll(filepath.Join(s.cfg.Root, "miniapps", appID))
}

func (s *Service) moveToTrash(appID string) error {
	src := filepath.Join(s.cfg.Root, "miniapps", appID)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dir := filepath.Join(s.cfg.Root, "trash", "miniapps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s-%d", appID, s.now().UnixNano()))
	return os.Rename(src, dst)
}

func encodeFiles(files []File) ([]byte, error) { return json.Marshal(files) }

func decodeFiles(raw []byte) ([]File, error) {
	var files []File
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, err
	}
	return files, nil
}
