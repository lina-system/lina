// SPDX-License-Identifier: Apache-2.0
// crw-takeover is an offline operator tool, never a LINA release command.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/tools/crw-takeover/importer"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crw-takeover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("state-root", "", "installed LINA state root; Core must be stopped")
	source := fs.String("snapshot", "", "read-only version 1 export")
	hash := fs.String("sha256", "", "independently verified snapshot SHA-256")
	person := fs.String("person", "", "LINA owner person id")
	agent := fs.String("agent", "", "existing LINA agent id")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 || *root == "" || *source == "" || *hash == "" || *person == "" || *agent == "" {
		fmt.Fprintln(stderr, "usage: crw-takeover --state-root DIR --snapshot FILE --sha256 HASH --person ID --agent ID")
		return 64
	}
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	f, err := os.Open(*source)
	if err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		f.Close()
		return fail(errors.New("snapshot must be a regular file of at most 64 MiB"))
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return fail(err)
	}
	if err = importer.Verify(data, *hash); err != nil {
		return fail(err)
	}
	ctl, err := ctlfiles.Open(*root)
	if err != nil {
		return fail(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		return fail(err)
	}
	if err = backup.CheckRestore(*root); err != nil {
		return fail(err)
	}
	path := filepath.Join(*root, "canon/canon.sqlite")
	version, epoch, err := canon.Inspect(ctx, path)
	if err != nil {
		return fail(err)
	}
	if version == 0 || epoch == 0 {
		return fail(errors.New("start the installed Core once before takeover"))
	}
	db, err := canon.Open(ctx, canon.Config{Path: path, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		return fail(err)
	}
	defer db.Close(context.Background())
	if err = db.Read(ctx, func(tx *canon.Tx) error {
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT r.person_id FROM agents a JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=1 WHERE a.id=? AND a.trashed_at=0`, *agent).Scan(&owner)
		if err != nil {
			return err
		}
		if owner != *person {
			return errors.New("agent is not owned by the named person")
		}
		return nil
	}); err != nil {
		return fail(err)
	}
	repo, err := records.Open(*root, db)
	if err != nil {
		return fail(err)
	}
	defer repo.Close()
	journal := filepath.Join(*root, "system/crw-takeover-journal.json")
	report, importErr := importer.Import(ctx, repo, records.Actor{Person: *person, Agent: *agent}, data, *hash, journal)
	if err = json.NewEncoder(stdout).Encode(report); err != nil {
		return fail(errors.Join(importErr, err))
	}
	if importErr != nil {
		return fail(importErr)
	}
	return 0
}
