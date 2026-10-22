// SPDX-License-Identifier: Apache-2.0
package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

type Config struct {
	StateRoot string
	Archive   string
	SHA256    string
	// RequiredManifest defaults to the installed manifest, when present.
	RequiredManifest string
}
type Prepared struct {
	Directory      string          `json:"directory"`
	Version        string          `json:"version"`
	SHA256         string          `json:"sha256"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	Preflight      PreflightResult `json:"preflight"`
	Gate           Gate            `json:"gate"`
}

// Prepare preserves the active composition. A content-addressed directory is
// created exclusively and only a successful candidate receives prepared.json.
// It never treats preparation as an idle fence, backup, smoke or activation.
func Prepare(ctx context.Context, cfg Config) (prepared Prepared, err error) {
	prepared.Gate = Gate{Idle: Unknown, Candidate: Unknown, Schema: Unknown, Peers: Unknown, Space: Unknown, Generation: Unknown}
	cfg.StateRoot, err = filepath.Abs(cfg.StateRoot)
	if err != nil {
		return prepared, err
	}
	for _, name := range []string{cfg.StateRoot, filepath.Join(cfg.StateRoot, "system")} {
		info, err := os.Lstat(name)
		if err != nil {
			return prepared, err
		}
		if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
			return prepared, err
		}
	}
	versions := filepath.Join(cfg.StateRoot, "system/updates")
	if err := os.Mkdir(versions, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return prepared, err
	}
	info, err := os.Lstat(versions)
	if err != nil {
		return prepared, err
	}
	if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
		return prepared, err
	}
	var space syscall.Statfs_t
	if err := syscall.Statfs(versions, &space); err != nil {
		return prepared, err
	}
	if uint64(space.Bavail) < uint64((archiveLimit+unpackLimit)/int64(space.Bsize)+1) {
		prepared.Gate.Space = Fail
		return prepared, errors.New("insufficient update staging space")
	}
	prepared.Gate.Space = Pass
	workspace, err := os.MkdirTemp(versions, ".prepare-")
	if err != nil {
		return prepared, err
	}
	defer os.RemoveAll(workspace)
	archive, err := verifiedArchive(ctx, cfg.Archive, cfg.SHA256, workspace)
	if err != nil {
		prepared.Gate.Candidate = Fail
		return prepared, err
	}
	directory := filepath.Join(versions, cfg.SHA256)
	// No rename-over-existing and no reuse of a partial attempt. Existing versions
	// belong to their creator and are retained, even when not prepared.
	if err := os.Mkdir(directory, 0700); err != nil {
		return prepared, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(directory)
		}
	}()
	if err := unpack(ctx, archive, directory); err != nil {
		return prepared, err
	}
	metadata := filepath.Join(directory, "manifest.json")
	m, err := verifyComposition(ctx, directory)
	if err != nil {
		prepared.Gate.Candidate = Fail
		return prepared, err
	}
	prepared.Gate.Candidate = Pass
	if cfg.RequiredManifest == "" {
		installed := filepath.Join(cfg.StateRoot, "system/bin/manifest.json")
		if _, err := os.Lstat(installed); err == nil {
			cfg.RequiredManifest = installed
		} else if !errors.Is(err, os.ErrNotExist) {
			return prepared, err
		}
	}
	args := []string{"core", "--preflight", "--state-root", cfg.StateRoot, "--manifest", metadata}
	if cfg.RequiredManifest != "" {
		args = append(args, "--required-manifest", cfg.RequiredManifest)
	}
	owner := proc.NewOwner()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, owner.Close(cleanup))
	}()
	deadline, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	registry := lifecycle.NewRegistry(nil)
	activity, err := registry.Acquire(deadline, lease.Activity{Class: lease.Live, Reason: "candidate update preflight"})
	if err != nil {
		return prepared, err
	}
	defer activity.Release()
	var stdout, stderr boundedOutput
	if err := owner.Run(deadline, activity, proc.Command{Path: filepath.Join(directory, "lina"), Args: args, Dir: workspace, Env: []string{"HOME=" + workspace, "TMPDIR=" + workspace, "PATH=", "LANG=C"}, Stdout: &stdout, Stderr: &stderr}); err != nil {
		return prepared, fmt.Errorf("candidate preflight refused: %w: %s", err, stderr.String())
	}
	if stdout.overflow || stderr.overflow {
		return prepared, errors.New("candidate preflight output exceeds limit")
	}
	if err := json.Unmarshal(stdout.Bytes(), &prepared.Preflight); err != nil {
		return prepared, fmt.Errorf("invalid preflight result: %w", err)
	}
	p := prepared.Preflight
	if p.StoreSchema < 0 || p.BinarySchema < 1 || p.StoreSchema > p.BinarySchema || p.PeerRows < 1 || (p.StoreSchema == p.BinarySchema && p.SchemaRelation != "equal") || (p.StoreSchema < p.BinarySchema && p.SchemaRelation != "forward") {
		return prepared, errors.New("incomplete candidate preflight result")
	}
	manifestBytes, err := os.ReadFile(metadata)
	if err != nil {
		return prepared, err
	}
	prepared.ManifestSHA256 = fmt.Sprintf("%x", sha256.Sum256(manifestBytes))
	prepared.Directory, prepared.Version, prepared.SHA256 = directory, m.Version, cfg.SHA256
	prepared.Gate.Schema, prepared.Gate.Peers = Pass, Pass
	data, err := json.Marshal(prepared)
	if err != nil {
		return prepared, err
	}
	file, err := os.OpenFile(filepath.Join(directory, "prepared.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return prepared, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return prepared, err
	}
	// Sync nested directories as well as publication in system/updates.
	if err := syncDirectories(directory); err != nil {
		return prepared, err
	}
	parent, err := os.Open(versions)
	if err != nil {
		return prepared, err
	}
	return prepared, errors.Join(parent.Sync(), parent.Close())
}

func verifyComposition(ctx context.Context, directory string) (manifest.Manifest, error) {
	m, err := manifest.Read(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return m, err
	}
	if m.Target != runtime.GOOS+"/"+runtime.GOARCH {
		return m, errors.New("candidate target does not match this host")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return m, err
	}
	var extra struct {
		InstallFiles []manifest.Binary `json:"installFiles"`
	}
	if err := json.Unmarshal(raw, &extra); err != nil {
		return m, err
	}
	artifacts := append(append([]manifest.Binary{}, m.Binaries...), extra.InstallFiles...)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return m, err
	}
	defer root.Close()
	seen := map[string]bool{}
	for i, artifact := range artifacts {
		if i >= len(m.Binaries) && (!strings.HasPrefix(artifact.Name, "adapters/claude/") || !filepath.IsLocal(artifact.Name) || filepath.ToSlash(filepath.Clean(artifact.Name)) != artifact.Name || strings.Contains(artifact.Name, "\\")) {
			return m, errors.New("invalid adapter artifact")
		}
		digest, err := hex.DecodeString(artifact.SHA256)
		if err != nil || len(digest) != sha256.Size || artifact.Size < 1 || artifact.Size > fileLimit || seen[artifact.Name] {
			return m, errors.New("invalid composition artifact")
		}
		seen[artifact.Name] = true
		file, err := root.Open(artifact.Name)
		if err != nil {
			return m, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
			file.Close()
			return m, errors.New("artifact size/type mismatch")
		}
		if artifact.Name == "lina" && info.Mode().Perm()&0100 == 0 {
			file.Close()
			return m, errors.New("candidate Core is not executable")
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, contextReader{ctx, file})
		if err := errors.Join(readErr, file.Close()); err != nil {
			return m, err
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != artifact.SHA256 {
			return m, fmt.Errorf("artifact digest mismatch: %s", artifact.Name)
		}
	}
	if len(extra.InstallFiles) > 0 && (!seen["adapters/claude/package.json"] || !seen["adapters/claude/package-lock.json"]) {
		return m, errors.New("Claude adapter requires pinned package and lockfile")
	}
	return m, nil
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (64 << 10) - b.Len()
	if n > remaining {
		data = data[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}
func syncDirectories(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		directory, err := os.Open(path)
		if err != nil {
			return err
		}
		return errors.Join(directory.Sync(), directory.Close())
	})
}
