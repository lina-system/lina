// SPDX-License-Identifier: Apache-2.0
package murmur

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// spoolPath and configPath are the only files a run reads from the person's
// data home. The derived database, vectors and model cache are never opened.
const (
	spoolPath  = "spool/events.jsonl"
	configPath = "config.json"
)

// snapshot is the verified copy of a stopped data home that one run reads. The
// source is only ever copied, never opened for writing, moved or deleted.
type snapshot struct {
	spool, config         string
	spoolHash, configHash string
	spoolSize             int64
}

// copySnapshot copies the two files of a data home into staging and verifies the
// copy against the source. It refuses a home whose files are missing, not
// regular, a link, or changed while they were copied.
func copySnapshot(home, staging string) (snapshot, error) {
	if home == "" || staging == "" {
		return snapshot{}, errors.New("a data home and a staging directory are required")
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return snapshot{}, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return snapshot{}, err
	}
	staged, err := filepath.Abs(staging)
	if err != nil {
		return snapshot{}, err
	}
	if within(resolved, staged) || within(staged, resolved) {
		return snapshot{}, errors.New("the staging directory must be outside the data home")
	}
	result := snapshot{}
	for _, item := range []struct {
		relative string
		dest     *string
		hash     *string
	}{
		{spoolPath, &result.spool, &result.spoolHash},
		{configPath, &result.config, &result.configHash},
	} {
		source := filepath.Join(resolved, filepath.FromSlash(item.relative))
		before, hash, size, err := fingerprint(source)
		if err != nil {
			return snapshot{}, fmt.Errorf("%s: %w", item.relative, err)
		}
		target := filepath.Join(staged, filepath.Base(item.relative)+".copy")
		copied, err := copyFile(source, target)
		if err != nil {
			return snapshot{}, fmt.Errorf("%s: %w", item.relative, err)
		}
		if copied != hash {
			return snapshot{}, fmt.Errorf("%s: the copy does not match the source", item.relative)
		}
		after, _, _, err := fingerprint(source)
		if err != nil {
			return snapshot{}, fmt.Errorf("%s: %w", item.relative, err)
		}
		if after != before {
			return snapshot{}, fmt.Errorf("%s: the source changed while it was copied; stop the collector and try again", item.relative)
		}
		*item.dest, *item.hash = target, copied
		if item.relative == spoolPath {
			result.spoolSize = size
		}
	}
	return result, nil
}

// fingerprint identifies a source file without reading it twice. A symlink and a
// directory are refused: only the named files may be handed over.
func fingerprint(path string) (string, string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", 0, errors.New("must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer file.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, file)
	if err != nil {
		return "", "", 0, err
	}
	identity := fmt.Sprintf("%d:%d:%v", info.ModTime().UnixNano(), size, info.Mode().Perm())
	return identity, hex.EncodeToString(sum.Sum(nil)), size, nil
}

// copyFile copies one source file into staging and returns the copy's SHA-256.
func copyFile(source, target string) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, sum), in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		os.Remove(target)
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func within(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
