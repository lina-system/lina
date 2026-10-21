// SPDX-License-Identifier: Apache-2.0

// Package backup publishes whole, verified generations. Its caller owns Core's
// exclusive state-root lock and quiesces all writers for the entire call.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/retract"
)

var ErrInvalid = errors.New("invalid_backup")
var ErrHashMismatch = errors.New("backup_hash_mismatch")

type Config struct {
	Root      string
	Canon     *canon.Store
	Controls  canon.ControlFiles
	MainID    string
	Epoch     uint64
	Artifacts map[string]string
	boundary  func(string) error
}

type File struct {
	Path string `json:"path"`
	Hash string `json:"sha256"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
	Link string `json:"link,omitempty"`
}
type SQLite struct {
	File
	Revision      string `json:"revision"`
	SchemaVersion int    `json:"schema_version"`
}
type Repository struct {
	Path string `json:"path"`
	Head string `json:"head"`
}
type Derived struct {
	Path           string `json:"path"`
	AssetID        string `json:"asset_id"`
	SourceRevision string `json:"source_revision"`
}
type Manifest struct {
	SchemaVersion       int               `json:"schema_version"`
	GenerationID        string            `json:"generation_id"`
	MainID              string            `json:"main_id"`
	CreatedAt           time.Time         `json:"created_at"`
	Artifacts           map[string]string `json:"artifacts"`
	Epoch               string            `json:"epoch"`
	RevocationWatermark uint64            `json:"revocation_watermark"`
	SQLite              SQLite            `json:"sqlite"`
	Repositories        []Repository      `json:"repositories"`
	Assets              []assets.Revision `json:"assets"`
	Derived             []Derived         `json:"derived"`
	Files               []File            `json:"files"`
	Directories         []string          `json:"directories"`
}

func Create(ctx context.Context, cfg Config) (result Manifest, err error) {
	if cfg.Root == "" || cfg.Canon == nil || cfg.Controls == nil || !cfg.Controls.Locked() || cfg.MainID == "" || cfg.Epoch == 0 || len(cfg.Artifacts) == 0 {
		return result, ErrInvalid
	}
	boundary := func(stage string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cfg.boundary != nil {
			return cfg.boundary(stage)
		}
		return nil
	}
	if err = boundary("removals"); err != nil {
		return result, err
	}
	removals, err := retract.Open(ctx, retract.Config{Canon: cfg.Canon, Root: cfg.Root})
	if err != nil {
		return result, err
	}
	defer removals.Close()
	watermark, err := removals.Epoch(ctx)
	if err != nil {
		return result, err
	}
	source, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return result, err
	}
	defer source.Close()
	if err = privateDir(source, "backups"); err != nil {
		return result, err
	}
	id := "generation-" + rand.Text()
	relative := filepath.Join("backups", id)
	if err = source.Mkdir(relative, 0700); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, source.RemoveAll(relative))
		}
	}()
	dest, err := source.OpenRoot(relative)
	if err != nil {
		return result, err
	}
	defer dest.Close()
	m := Manifest{SchemaVersion: 1, GenerationID: id, MainID: cfg.MainID, CreatedAt: time.Now().UTC(), Artifacts: cfg.Artifacts, Epoch: strconv.FormatUint(cfg.Epoch, 10), RevocationWatermark: watermark, Repositories: []Repository{}, Assets: []assets.Revision{}, Derived: []Derived{}, Files: []File{}, Directories: []string{}}
	if err = boundary("snapshot"); err != nil {
		return result, err
	}
	if err = privateDir(dest, "canon"); err != nil {
		return result, err
	}
	if err = regularPath(source, "canon/canon.sqlite"); err != nil {
		return result, err
	}
	reader, err := canon.OpenSQLiteReader(ctx, filepath.Join(cfg.Root, "canon/canon.sqlite"))
	if err != nil {
		return result, err
	}
	err = canon.BackupSQLite(ctx, reader, filepath.Join(cfg.Root, relative, "canon/canon.sqlite"))
	err = errors.Join(err, reader.Close())
	if err != nil {
		return result, err
	}
	snapshot, err := canon.OpenSQLite(ctx, filepath.Join(cfg.Root, relative, "canon/canon.sqlite"))
	if err != nil {
		return result, err
	}
	// The completed snapshot is standalone: closing readers must not leave WAL
	// indexes or create mutable sidecars inside an immutable generation.
	var mode string
	err = snapshot.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode)
	if err == nil && mode != "delete" {
		err = ErrInvalid
	}
	if err == nil {
		err = snapshot.QueryRowContext(ctx, "PRAGMA user_version").Scan(&m.SQLite.SchemaVersion)
	}
	var integrity string
	if err == nil {
		err = snapshot.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
		if err == nil && integrity != "ok" {
			err = ErrInvalid
		}
	}
	err = errors.Join(err, snapshot.Close())
	if err != nil {
		return result, err
	}
	m.SQLite.File, err = fingerprint(ctx, dest, "canon/canon.sqlite")
	if err != nil {
		return result, err
	}
	m.SQLite.Revision = m.SQLite.Hash
	m.Files = append(m.Files, m.SQLite.File)
	m.Directories = append(m.Directories, ".", "canon")
	store, err := assets.Open(ctx, cfg.Root, cfg.Canon)
	if err != nil {
		return result, err
	}
	m.Assets, err = store.Backup(ctx, filepath.Join(cfg.Root, relative))
	err = errors.Join(err, store.Close())
	if err != nil {
		return result, err
	}
	for _, a := range m.Assets {
		m.Files = append(m.Files, File{Path: filepath.ToSlash(a.Path), Hash: a.Hash, Size: a.Size, Mode: 0600})
		for dir := filepath.Dir(a.Path); dir != "."; dir = filepath.Dir(dir) {
			m.Directories = append(m.Directories, filepath.ToSlash(dir))
		}
	}
	m.Repositories, err = repositories(ctx, source, filepath.Join(cfg.Root, relative, "canon/canon.sqlite"))
	if err != nil {
		return result, err
	}
	m.Derived, err = derivedRevisions(source)
	if err != nil {
		return result, err
	}
	if err = boundary("copy"); err != nil {
		return result, err
	}
	for _, area := range []string{"versions", "derived", "workspace"} {
		if err = copyTree(ctx, source, dest, area, boundary, &m.Files, &m.Directories); err != nil {
			return result, err
		}
	}
	if err = boundary("copied"); err != nil {
		return result, err
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	sort.Strings(m.Directories)
	unique := m.Directories[:0]
	for _, dir := range m.Directories {
		if len(unique) == 0 || unique[len(unique)-1] != dir {
			unique = append(unique, dir)
		}
	}
	m.Directories = unique
	if err = boundary("verify"); err != nil {
		return result, err
	}
	if err = verify(ctx, dest, m); err != nil {
		return result, err
	}
	if err = boundary("publish"); err != nil {
		return result, err
	}
	// Every data file and directory is durable before the atomic manifest rename.
	for i := len(m.Directories) - 1; i >= 0; i-- {
		if err = syncDir(dest, m.Directories[i]); err != nil {
			return result, err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return result, err
	}
	f, err := dest.OpenFile(".manifest.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return result, err
	}
	if err = dest.Rename(".manifest.tmp", "manifest.json"); err != nil {
		return result, err
	}
	if err = syncDir(dest, "."); err != nil {
		return result, err
	}
	if err = syncDir(source, "backups"); err != nil {
		return result, err
	}
	return m, nil
}

// Verify reads a published manifest and checks the complete inventory against
// the stored hashes, including SQLite and every material revision.
func Verify(ctx context.Context, path string) (Manifest, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	if err = regularPath(root, "manifest.json"); err != nil {
		return Manifest{}, err
	}
	data, err := root.ReadFile("manifest.json")
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if err = verify(ctx, root, m); err != nil {
		return Manifest{}, fmt.Errorf("verify generation: %w", err)
	}
	return m, nil
}
