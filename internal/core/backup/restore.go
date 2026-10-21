// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

type RestoreConfig struct {
	Root, Generation string
	Migrations       []canon.Migration
	Artifacts        map[string]string
	boundary         func(string) error
}

type restorePlan struct {
	Generation string   `json:"generation"`
	Manifest   Manifest `json:"manifest"`
}

// Restore verifies and prepares one whole generation with the main stopped.
// Only the captured areas and the append-only removal list are published.
func Restore(ctx context.Context, cfg RestoreConfig) (result Manifest, err error) {
	if cfg.Root == "" || cfg.Generation == "" || len(cfg.Migrations) == 0 || len(cfg.Artifacts) == 0 {
		return result, ErrInvalid
	}
	targetPath, err := filepath.Abs(cfg.Root)
	if err != nil {
		return result, err
	}
	generationPath, err := filepath.Abs(cfg.Generation)
	if err != nil {
		return result, err
	}
	controls, err := ctlfiles.Open(targetPath)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, controls.Close()) }()
	if err = controls.Acquire(); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(targetPath)
	if err != nil {
		return result, err
	}
	defer root.Close()
	identity, err := controls.Read("main.json")
	if err != nil {
		return result, err
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(identity, &main); err != nil || main.ID == "" {
		return result, ErrInvalid
	}
	boundary := func(at string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return restoreBoundary(cfg.boundary, at)
	}
	if pending := CheckRestore(targetPath); pending != nil {
		if !errors.Is(pending, ErrRestorePending) {
			return result, pending
		}
		if err := regularPath(root, restorePending+"/plan.json"); err != nil {
			return result, err
		}
		data, e := root.ReadFile(restorePending + "/plan.json")
		if e != nil {
			return result, e
		}
		var plan restorePlan
		if json.Unmarshal(data, &plan) != nil || plan.Generation != generationPath || plan.Manifest.MainID != main.ID || !maps.Equal(plan.Manifest.Artifacts, cfg.Artifacts) {
			return result, ErrRestorePending
		}
		if err := resumeRestore(root, boundary); err != nil {
			return result, err
		}
		return plan.Manifest, nil
	}
	// A generation moved to an archive still has its sibling removal list. If
	// it remains in an installed main, exclude that main's writer as well.
	sourceState := filepath.Dir(filepath.Dir(generationPath))
	if sourceState != targetPath && filepath.Base(filepath.Dir(generationPath)) == "backups" {
		if _, e := os.Lstat(filepath.Join(sourceState, "system/core/core.lock")); e == nil {
			sourceControls, e := ctlfiles.Open(sourceState)
			if e != nil {
				return result, e
			}
			defer sourceControls.Close()
			if e = sourceControls.Acquire(); e != nil {
				return result, e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return result, e
		}
	}
	m, err := Verify(ctx, generationPath)
	if err != nil {
		return result, err
	}
	if m.MainID != main.ID || !maps.Equal(m.Artifacts, cfg.Artifacts) || m.SQLite.SchemaVersion > len(cfg.Migrations) {
		return result, errors.New("incompatible restore identity, artifacts or schema")
	}
	captured, err := strconv.ParseUint(m.Epoch, 10, 64)
	if err != nil || strconv.FormatUint(captured, 10) != m.Epoch {
		return result, ErrInvalid
	}
	if err := validateRestoreLayout(m); err != nil {
		return result, err
	}
	if info, e := root.Lstat("canon"); e == nil && !info.IsDir() {
		return result, ErrInvalid
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return result, e
	}
	current := filepath.Join(targetPath, "canon/canon.sqlite")
	epoch, err := chooseRestoreEpoch(ctx, controls, current, captured)
	if err != nil {
		return result, err
	}
	source, err := os.OpenRoot(generationPath)
	if err != nil {
		return result, err
	}
	defer source.Close()
	journals, err := os.OpenRoot(filepath.Dir(generationPath))
	if err != nil {
		return result, err
	}
	defer journals.Close()
	if err := regularPath(journals, "removals"); err != nil {
		return result, err
	}
	sourceList, err := journals.ReadFile("removals")
	if err != nil {
		return result, err
	}
	var targetList []byte
	if _, e := root.Lstat("backups/removals"); e == nil {
		if err := regularPath(root, "backups/removals"); err != nil {
			return result, err
		}
		targetList, err = root.ReadFile("backups/removals")
		if err != nil {
			return result, err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, e
	}
	removals, err := mergeRestoreRemovals(sourceList, targetList)
	if err != nil {
		return result, err
	}
	if err := checkRestoreHistory(ctx, removals, current, filepath.Dir(generationPath), m); err != nil {
		return result, err
	}
	for _, dir := range []string{"staging", "backups"} {
		if err := privateDir(root, dir); err != nil {
			return result, err
		}
	}
	prepared := "staging/restore-" + rand.Text()
	if err := privateDir(root, prepared+"/new"); err != nil {
		return result, err
	}
	// Until publication's durable marker exists, only our unpublished copy can
	// be removed. After the rename the pending directory belongs to recovery.
	defer root.RemoveAll(prepared)
	dest, err := root.OpenRoot(prepared + "/new")
	if err != nil {
		return result, err
	}
	defer dest.Close()
	files, dirs := []File{}, []string{}
	for _, area := range []string{"canon", "versions", "materials", "derived", "workspace"} {
		if err := copyTree(ctx, source, dest, area, boundary, &files, &dirs); err != nil {
			return result, err
		}
	}
	if err := verify(ctx, dest, m); err != nil {
		return result, err
	}
	stagePath := filepath.Join(targetPath, prepared, "new")
	if err := verifyRestoreCanon(ctx, stagePath, m); err != nil {
		return result, err
	}
	for _, dir := range []string{"versions", "materials", "derived", "workspace", "backups"} {
		if err := privateDir(dest, dir); err != nil {
			return result, err
		}
	}
	if err := writeRestoreFile(dest, "backups/removals", removals); err != nil {
		return result, err
	}
	if err := prepareRestoredCanon(ctx, stagePath, epoch, cfg.Migrations); err != nil {
		return result, err
	}
	if err := privateDir(root, prepared+"/previous/backups"); err != nil {
		return result, err
	}
	data, err := json.Marshal(restorePlan{Generation: generationPath, Manifest: m})
	if err != nil {
		return result, err
	}
	if err := writeRestoreFile(root, prepared+"/plan.json", data); err != nil {
		return result, err
	}
	if err := syncRestoreTree(root, prepared); err != nil {
		return result, err
	}
	if err := boundary("prepared"); err != nil {
		return result, err
	}
	if err := commitRestore(root, prepared, boundary); err != nil {
		return result, err
	}
	return m, nil
}

func writeRestoreFile(root *os.Root, path string, data []byte) error {
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
