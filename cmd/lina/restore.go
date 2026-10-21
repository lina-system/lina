// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/turn"
)

func runRestore(args []string) int {
	flags := flag.NewFlagSet("lina restore", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "installed private target state root for the same main")
	generation := flags.String("generation", "", "published generation directory with its sibling removals file")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina restore --generation <path> --state-root <path>\nStop Core first. Keep the generation with its sibling removals file; the target's installation and control files are preserved.")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		flags.Usage()
		return 64
	}
	if *root == "" || *generation == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := backupRegularPath(*root, "system/bin/manifest.json"); err != nil {
		fmt.Fprintf(os.Stderr, "lina restore: %v\n", err)
		return 78
	}
	installed, err := manifest.Read(filepath.Join(*root, "system/bin/manifest.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina restore: %v\n", err)
		return 78
	}
	artifacts := map[string]string{"backup_command": buildinfo.String(), "kit": strconv.FormatInt(installed.KitVersion, 10), "sqlite_wasm": canon.SQLiteWASMSHA256}
	for _, binary := range installed.Binaries {
		artifacts[binary.Name] = installed.Version + "@" + installed.Commit
	}
	migrations := canon.Migrations()
	for _, schema := range []string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema} {
		migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: schema})
	}
	restored, err := backup.Restore(ctx, backup.RestoreConfig{Root: *root, Generation: *generation, Migrations: migrations, Artifacts: artifacts})
	if errors.Is(err, ctlfiles.ErrLocked) {
		fmt.Fprintln(os.Stderr, "lina restore: Core is running. Stop Core before restoring.")
		return 69
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina restore: %v\n", err)
		return 78
	}
	fmt.Fprintf(os.Stdout, "Verified restore: %s (captured %s)\n", restored.GenerationID, restored.CreatedAt.Format("2006-01-02T15:04:05Z"))
	return 0
}
