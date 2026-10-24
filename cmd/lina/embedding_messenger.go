// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

func runMessengerEmbedding(args []string) int {
	flags := flag.NewFlagSet("lina embedding messenger", flag.ContinueOnError)
	root := flags.String("state-root", "", "installed state root; stop Core before configuring")
	person := flags.String("person", "", "installed person id")
	platform := flags.String("platform", "", "messenger platform")
	account := flags.String("account", "", "messenger account")
	enable := flags.Bool("enable", false, "allow this source's eligible messages to reach the configured embedding endpoint")
	disable := flags.Bool("disable", false, "revoke this source's embedding consent")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if *root == "" || *person == "" || *platform == "" || *account == "" || *enable == *disable || flags.NArg() != 0 {
		return 64
	}
	if err := configureMessengerEmbedding(context.Background(), *root, *person, *platform, *account, *enable); err != nil {
		if errors.Is(err, ctlfiles.ErrLocked) {
			fmt.Fprintln(os.Stderr, "Stop Core before configuring embeddings.")
			return 69
		}
		fmt.Fprintln(os.Stderr, "Could not save messenger embedding consent.")
		return 78
	}
	fmt.Fprintln(os.Stdout, "Messenger embedding consent saved.")
	return 0
}

func configureMessengerEmbedding(ctx context.Context, root, person, platform, account string, enabled bool) error {
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return err
	}
	defer controls.Close()
	if err := controls.Acquire(); err != nil {
		return err
	}
	path := filepath.Join(root, "canon/canon.sqlite")
	stored, err := canon.PeekEpoch(ctx, path)
	if err != nil {
		return err
	}
	epoch, err := controls.Epoch()
	if err != nil {
		return err
	}
	epoch = max(epoch, stored)
	if epoch == 0 {
		return errors.New("start Core before configuring embeddings")
	}
	db, err := canon.Open(ctx, canon.Config{Path: path, Epoch: epoch, Controls: controls, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		return err
	}
	defer db.Close(context.Background())
	return messenger.Open(db, nil, nil).SetEmbedding(ctx, person, platform, account, enabled)
}
