// SPDX-License-Identifier: Apache-2.0
package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/manifest"
)

// StageActivation rechecks prepared evidence and copies only release-owned
// components. User-installed workers and their configuration are never moved.
func StageActivation(ctx context.Context, root, socket, candidate string) (r ctlfiles.UpdateRecord, err error) {
	root, err = filepath.Abs(root)
	if err != nil {
		return r, err
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return r, err
	}
	data, err := os.ReadFile(filepath.Join(candidate, "prepared.json"))
	if err != nil {
		return r, err
	}
	var p Prepared
	if err = json.Unmarshal(data, &p); err != nil {
		return r, err
	}
	digest, e := hex.DecodeString(p.SHA256)
	if e != nil || len(digest) != 32 || p.Directory != candidate || candidate != filepath.Join(root, "system/updates", p.SHA256) || p.Gate.Candidate != Pass || p.Gate.Schema != Pass || p.Gate.Peers != Pass || p.Gate.Space != Pass || p.Preflight.UpdateProtocol != 1 {
		return r, errors.New("update_candidate_invalid")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(candidate, "manifest.json"))
	if err != nil {
		return r, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(manifestBytes)) != p.ManifestSHA256 {
		return r, errors.New("update_candidate_invalid")
	}
	m, err := verifyComposition(ctx, candidate)
	if err != nil {
		return r, err
	}
	old, err := VerifyInstalled(ctx, root)
	if err != nil {
		return r, err
	}
	r = ctlfiles.UpdateRecord{Schema: 1, ID: rand.Text(), Root: root, Socket: socket, Phase: "draining", Candidate: candidate, ForwardMigration: p.Preflight.SchemaRelation == "forward"}
	r.Previous = filepath.Join(root, "system/updates", "previous-"+r.ID)
	for _, b := range m.Binaries {
		if b.Name == "lina" {
			r.CandidateDigest = b.SHA256
		}
	}
	for _, b := range old.Binaries {
		if b.Name == "lina" {
			r.PreviousDigest = b.SHA256
		}
	}
	if r.CandidateDigest == "" || r.PreviousDigest == "" {
		return r, errors.New("update_candidate_invalid")
	}
	// Keep the service's installed optional-worker shape; adding/removing a unit
	// is a separate install change, not an implicit update side effect.
	if hasWorker(m) != hasWorker(old) {
		return r, errors.New("update_worker_composition_changed")
	}
	next := r.Previous + "-next"
	if err = os.Mkdir(next, 0700); err != nil {
		return r, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(next)
		}
	}()
	if err = os.Mkdir(filepath.Join(next, "bin"), 0700); err != nil {
		return r, err
	}
	for _, b := range m.Binaries {
		if b.Name == "lina" || b.Name == "moirai-worker" {
			if err = copyArtifact(ctx, filepath.Join(candidate, b.Name), filepath.Join(next, "bin", b.Name), 0700); err != nil {
				return r, err
			}
		}
	}
	if err = copyArtifact(ctx, filepath.Join(candidate, "manifest.json"), filepath.Join(next, "bin/manifest.json"), 0600); err != nil {
		return r, err
	}
	if _, e := os.Lstat(filepath.Join(candidate, "adapters")); e == nil {
		err = copyTree(ctx, filepath.Join(candidate, "adapters"), filepath.Join(next, "adapters"))
		if err != nil {
			return r, err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return r, e
	}
	if _, err = verifyInstalledSystem(ctx, next); err != nil {
		return r, err
	}
	if err = syncDirectories(next); err != nil {
		return r, err
	}
	return r, syncDirectory(filepath.Dir(next))
}
func hasWorker(m manifest.Manifest) bool {
	for _, b := range m.Binaries {
		if b.Name == "moirai-worker" {
			return true
		}
	}
	return false
}

// VerifyInstalled uses the install layout (Node is distributed separately).
func VerifyInstalled(ctx context.Context, root string) (manifest.Manifest, error) {
	return verifyInstalledSystem(ctx, filepath.Join(root, "system"))
}
func verifyInstalledSystem(ctx context.Context, system string) (manifest.Manifest, error) {
	m, err := manifest.Read(filepath.Join(system, "bin/manifest.json"))
	if err != nil {
		return m, err
	}
	raw, err := os.ReadFile(filepath.Join(system, "bin/manifest.json"))
	if err != nil {
		return m, err
	}
	var extra struct {
		InstallFiles []manifest.Binary `json:"installFiles"`
	}
	if err = json.Unmarshal(raw, &extra); err != nil {
		return m, err
	}
	for _, b := range append(append([]manifest.Binary{}, m.Binaries...), extra.InstallFiles...) {
		name := filepath.Join("bin", b.Name)
		if strings.HasPrefix(b.Name, "adapters/claude/") || b.Name == "adapters/claude-acp/package.json" || b.Name == "adapters/claude-acp/package-lock.json" {
			name = b.Name
		} else if b.Name != "lina" && b.Name != "moirai-worker" {
			continue
		}
		if !filepath.IsLocal(b.Name) || filepath.Clean(b.Name) != b.Name {
			return m, errors.New("update_candidate_invalid")
		}
		f, e := os.Open(filepath.Join(system, name))
		if e != nil {
			return m, e
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return m, e
		}
		hash := sha256.New()
		_, e = io.Copy(hash, contextReader{ctx, f})
		e = errors.Join(e, f.Close())
		if e != nil {
			return m, e
		}
		if !info.Mode().IsRegular() || info.Size() != b.Size || fmt.Sprintf("%x", hash.Sum(nil)) != b.SHA256 {
			return m, errors.New("update_candidate_invalid")
		}
	}
	return m, nil
}

// Swap runs only after a compatible verified snapshot and under core.lock.
// Every rename is individually durable; the record already says swapping.
func Swap(r ctlfiles.UpdateRecord, after func(string) error) error {
	if err := os.Mkdir(r.Previous, 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(r.Root, "system/adapters")); errors.Is(err, os.ErrNotExist) {
		marker, err := os.OpenFile(filepath.Join(r.Previous, "no-adapters"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = errors.Join(marker.Sync(), marker.Close(), syncDirectory(r.Previous)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for _, name := range []string{"bin", "adapters"} {
		active := filepath.Join(r.Root, "system", name)
		prior := filepath.Join(r.Previous, name)
		next := filepath.Join(r.Previous+"-next", name)
		if _, err := os.Lstat(active); err == nil {
			if err := move(active, prior); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if after != nil {
			if err := after("kept-" + name); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(next); err == nil {
			if err := move(next, active); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if after != nil {
			if err := after("swapped-" + name); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreComposition is idempotent after every rename. Failed candidates stay
// available for diagnosis; no unrelated directory is overwritten or deleted.
func RestoreComposition(r ctlfiles.UpdateRecord) error {
	failed := r.Previous + "-failed"
	if err := os.Mkdir(failed, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	for _, name := range []string{"bin", "adapters"} {
		prior := filepath.Join(r.Previous, name)
		active := filepath.Join(r.Root, "system", name)
		if _, err := os.Lstat(prior); errors.Is(err, os.ErrNotExist) {
			if name == "adapters" {
				if _, e := os.Lstat(filepath.Join(r.Previous, "no-adapters")); e == nil {
					if _, e = os.Lstat(active); e == nil {
						if e = move(active, filepath.Join(failed, name)); e != nil {
							return e
						}
					} else if !errors.Is(e, os.ErrNotExist) {
						return e
					}
				} else if !errors.Is(e, os.ErrNotExist) {
					return e
				}
			}
			continue
		} else if err != nil {
			return err
		}
		if _, err := os.Lstat(active); err == nil {
			if err := move(active, filepath.Join(failed, name)); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := move(prior, active); err != nil {
			return err
		}
	}
	return nil
}
func move(from, to string) error {
	if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		return errors.New("update_destination_occupied")
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return errors.Join(syncDirectory(filepath.Dir(from)), syncDirectory(filepath.Dir(to)))
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func copyArtifact(ctx context.Context, from, to string, mode os.FileMode) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("update_nonregular_artifact")
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, contextReader{ctx, in})
	return errors.Join(err, out.Sync(), out.Close())
}
func copyTree(ctx context.Context, from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		if d.IsDir() {
			return os.Mkdir(target, 0700)
		}
		return copyArtifact(ctx, path, target, 0600)
	})
}
