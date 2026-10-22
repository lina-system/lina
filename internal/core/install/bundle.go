// SPDX-License-Identifier: Apache-2.0
package install

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

type artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type bundle struct {
	files    map[string][]byte
	verified map[string][]string
}

// readBundle checks the exact bytes that will be installed. Adapter files are
// optional until that component ships; when listed, all are digest-verified.
func readBundle(dir string) (bundle, error) {
	b := bundle{files: map[string][]byte{}}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return b, err
	}
	defer root.Close()
	raw, err := readRegular(root, "manifest.json", 1<<20)
	if err != nil {
		return b, err
	}
	var m manifest.Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return b, err
	}
	if err = m.Validate(); err != nil {
		return b, err
	}
	if m.Target != runtime.GOOS+"/"+runtime.GOARCH {
		return b, errors.New("bundle target does not match this host")
	}
	var extra struct {
		VerifiedTools map[string][]string `json:"verifiedTools"`
		InstallFiles  []artifact          `json:"installFiles"`
	}
	if err = json.Unmarshal(raw, &extra); err != nil {
		return b, err
	}
	b.verified = m.VerifiedWorkerVersions
	if b.verified == nil {
		// Older installers documented this name before using the build field.
		b.verified = extra.VerifiedTools
	}
	b.files["manifest.json"] = raw
	for _, binary := range m.Binaries {
		if binary.Name != "lina" && binary.Name != "moirai-worker" {
			continue
		}
		data, err := readArtifact(root, artifact(binary))
		if err != nil {
			return b, err
		}
		b.files[binary.Name] = data
	}
	if b.files["lina"] == nil {
		return b, errors.New("bundle has no Linux lina binary")
	}
	for _, file := range extra.InstallFiles {
		if path.Clean(file.Name) != file.Name || !strings.HasPrefix(file.Name, "adapters/claude/") || strings.Contains(file.Name, "\\") {
			return b, errors.New("invalid adapter artifact path")
		}
		if _, exists := b.files[file.Name]; exists {
			return b, errors.New("duplicate adapter artifact")
		}
		data, err := readArtifact(root, file)
		if err != nil {
			return b, err
		}
		b.files[file.Name] = data
	}
	if len(extra.InstallFiles) > 0 && (b.files["adapters/claude/package.json"] == nil || b.files["adapters/claude/package-lock.json"] == nil) {
		return b, errors.New("Claude adapter requires its package file and lockfile")
	}
	return b, nil
}

func readArtifact(root *os.Root, file artifact) ([]byte, error) {
	if file.Size <= 0 || file.Size > 128<<20 {
		return nil, errors.New("invalid artifact size")
	}
	data, err := readRegular(root, file.Name, file.Size)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != file.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != file.SHA256 {
		return nil, fmt.Errorf("artifact %s does not match its manifest", file.Name)
	}
	return data, nil
}

func readRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("bundle artifact must be a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("bundle artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("bundle artifact exceeds its size limit")
	}
	return data, nil
}
