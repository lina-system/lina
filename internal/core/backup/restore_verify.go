// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func validateRestoreLayout(m Manifest) error {
	allowed := func(path string) bool {
		if !fs.ValidPath(path) {
			return false
		}
		area, _, _ := strings.Cut(path, "/")
		switch area {
		case "canon", "versions", "materials", "derived", "workspace":
			return true
		}
		return path == "."
	}
	for _, dir := range m.Directories {
		if !allowed(dir) && !(dir == "system" && hasOperatorJournal(m)) {
			return ErrInvalid
		}
	}
	for _, file := range m.Files {
		if file.Path == operatorJournal && file.Link == "" && file.Mode == 0600 {
			continue
		}
		if !allowed(file.Path) || file.Path == "." || strings.HasPrefix(file.Path, "canon/") && file.Path != "canon/canon.sqlite" {
			return ErrInvalid
		}
	}
	return nil
}

// Check independent canonical references as well as the manifest's inventory.
func verifyRestoreCanon(ctx context.Context, root string, m Manifest) error {
	path := filepath.Join(root, m.SQLite.Path)
	version, epoch, err := canon.Inspect(ctx, path)
	if err != nil {
		return err
	}
	captured, _ := strconv.ParseUint(m.Epoch, 10, 64)
	if version != m.SQLite.SchemaVersion || epoch == 0 || epoch > captured {
		return ErrInvalid
	}
	db, err := canon.OpenSQLiteReader(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return ErrInvalid
	}
	var watermark uint64
	if err := db.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&watermark); err != nil {
		return err
	}
	if watermark != m.RevocationWatermark {
		return ErrInvalid
	}
	rows, err := db.QueryContext(ctx, "SELECT asset_id,revision,path,hash,size FROM asset_revisions ORDER BY asset_id,revision")
	if err != nil {
		return err
	}
	references := []assets.Revision{}
	for rows.Next() {
		var a assets.Revision
		if err := rows.Scan(&a.AssetID, &a.Revision, &a.Path, &a.Hash, &a.Size); err != nil {
			rows.Close()
			return err
		}
		references = append(references, a)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if !slices.Equal(references, m.Assets) {
		return ErrHashMismatch
	}
	return assets.VerifyBackup(ctx, root, references)
}

type restoreRemoval struct {
	Sequence int    `json:"sequence"`
	Digest   string `json:"digest"`
	Entry    struct {
		ID string `json:"id"`
	} `json:"entry"`
}

// A truncated list may be a valid chain by itself. Every available current or
// newer canon supplies independent acknowledgments that must still be present.
func checkRestoreHistory(ctx context.Context, list []byte, current, parent string, selected Manifest) error {
	var entries []restoreRemoval
	scanner := bufio.NewScanner(bytes.NewReader(list))
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var entry restoreRemoval
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			return retract.ErrJournal
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	paths := []string{current}
	live := map[string]bool{current: true}
	if filepath.Base(parent) == "backups" {
		sourceCurrent := filepath.Join(filepath.Dir(parent), "canon/canon.sqlite")
		paths = append(paths, sourceCurrent)
		live[sourceCurrent] = true
	}
	parents := []string{parent, filepath.Join(filepath.Dir(filepath.Dir(current)), "backups")}
	scanned := map[string]bool{}
	for _, directory := range parents {
		if scanned[directory] {
			continue
		}
		scanned[directory] = true
		siblings, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) && directory != parent {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range siblings {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			if _, err := os.Lstat(filepath.Join(path, "manifest.json")); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			m, err := Verify(ctx, path)
			if err != nil {
				return err
			}
			if m.MainID != selected.MainID {
				continue
			}
			paths = append(paths, filepath.Join(path, m.SQLite.Path))
		}
	}

	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := checkRestoreAcknowledgments(ctx, path, entries); err != nil {
			if live[path] && damagedRestoreCanon(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func checkRestoreAcknowledgments(ctx context.Context, path string, entries []restoreRemoval) error {
	db, err := canon.OpenSQLiteReader(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT sequence,id,digest FROM removal_applied ORDER BY sequence")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var id, digest string
		if err := rows.Scan(&sequence, &id, &digest); err != nil {
			return err
		}
		if sequence < 1 || sequence > len(entries) {
			return retract.ErrJournal
		}
		entry := entries[sequence-1]
		if entry.Sequence != sequence || entry.Entry.ID != id || entry.Digest != digest {
			return retract.ErrJournal
		}
	}
	return rows.Err()
}

func syncRestoreTree(root *os.Root, path string) error {
	var dirs []string
	if err := fs.WalkDir(root.FS(), path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(root, dirs[i]); err != nil {
			return err
		}
	}
	return syncDir(root, filepath.Dir(path))
}
