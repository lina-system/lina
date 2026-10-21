// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/turn"
)

func runBackup(args []string) int {
	flags := flag.NewFlagSet("lina backup", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "path to the installed local state root; stop Core before backup")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina backup --state-root <path>\nStop Core first: systemctl --user stop lina-core.service (or Ctrl+C for a foreground Core).")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.Usage()
			return 0
		}
		flags.Usage()
		return 64
	}
	if *root == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	m, err := captureBackup(ctx, *root)
	if errors.Is(err, ctlfiles.ErrLocked) {
		fmt.Fprintln(os.Stderr, "lina backup: Core is running. Stop Core first: systemctl --user stop lina-core.service (or Ctrl+C for a foreground Core), then rerun backup.")
		return 69
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina backup: %v\n", err)
		return 78
	}
	fmt.Fprintf(os.Stdout, "Verified backup: %s\n", filepath.Join(*root, "backups", m.GenerationID))
	return 0
}

func captureBackup(ctx context.Context, root string) (backup.Manifest, error) {
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return backup.Manifest{}, err
	}
	defer controls.Close()
	// Canon and generation files stay unopened until this stable lock excludes
	// Core and other backups. It remains held through verification and publication.
	if err = controls.Acquire(); err != nil {
		return backup.Manifest{}, err
	}
	if err := backup.CheckRestore(root); err != nil {
		return backup.Manifest{}, err
	}
	data, err := controls.Read("main.json")
	if err != nil {
		return backup.Manifest{}, err
	}
	var identity struct {
		MainID string `json:"main_id"`
	}
	if err = json.Unmarshal(data, &identity); err != nil {
		return backup.Manifest{}, err
	}
	if identity.MainID == "" {
		return backup.Manifest{}, errors.New("state root has no installed main identity")
	}
	for _, path := range []string{"canon/canon.sqlite", "system/bin/manifest.json"} {
		if err := backupRegularPath(root, path); err != nil {
			return backup.Manifest{}, err
		}
	}
	installed, err := manifest.Read(filepath.Join(root, "system/bin/manifest.json"))
	if err != nil {
		return backup.Manifest{}, err
	}
	artifacts := map[string]string{"backup_command": buildinfo.String(), "kit": strconv.FormatInt(installed.KitVersion, 10), "sqlite_wasm": canon.SQLiteWASMSHA256}
	for _, binary := range installed.Binaries {
		artifacts[binary.Name] = installed.Version + "@" + installed.Commit
	}
	path := filepath.Join(root, "canon/canon.sqlite")
	info, err := os.Lstat(path)
	if err != nil {
		return backup.Manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return backup.Manifest{}, backup.ErrInvalid
	}
	epoch, err := controls.Epoch()
	if err != nil {
		return backup.Manifest{}, err
	}
	stored, err := canon.PeekEpoch(ctx, path)
	if err != nil {
		return backup.Manifest{}, err
	}
	epoch = max(epoch, stored)
	if epoch == 0 {
		return backup.Manifest{}, errors.New("state root has no captured Core epoch")
	}
	// This command composes the same canon and conversation schemas as Core.
	migrations := canon.Migrations()
	for _, schema := range []string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema} {
		migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: schema})
	}
	db, err := canon.Open(ctx, canon.Config{Path: path, Epoch: epoch, Controls: controls, Migrations: migrations})
	if err != nil {
		return backup.Manifest{}, err
	}
	defer db.Close(context.Background())
	return backup.Create(ctx, backup.Config{Root: root, Canon: db, Controls: controls, MainID: identity.MainID, Epoch: epoch, Artifacts: artifacts})
}

// Reject links in protected input paths before opening SQLite for replay.
func backupRegularPath(path, name string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	parts := strings.Split(name, "/")
	current := "."
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if i == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return backup.ErrInvalid
			}
		} else if !info.IsDir() {
			return backup.ErrInvalid
		}
	}
	return nil
}
