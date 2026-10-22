// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
)

func runCore(args []string) int {
	flags := flag.NewFlagSet("lina core", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "path to the installed local state root")
	socket := flags.String("socket", "", "path to the private client Unix socket")
	flags.Usage = func() { fmt.Fprintln(os.Stderr, "usage: lina core --state-root <path> --socket <path>") }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		flags.Usage()
		return 64
	}
	if *root == "" || *socket == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if version, err := canon.SQLiteVectorVersion(); err != nil || version != canon.SQLiteVecVersion {
		if err == nil {
			err = fmt.Errorf("unexpected sqlite-vec version %q", version)
		}
		fmt.Fprintf(os.Stderr, "lina core: initialize SQLite: %v\n", err)
		return 78
	}
	route, err := modelpath.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina core: invalid model configuration")
		return 78
	}
	if err := boot.Run(ctx, boot.Config{StateRoot: *root, Socket: *socket, Conversation: route}); err != nil {
		if errors.Is(err, lifecycle.ErrRestart) {
			return 75
		}
		fmt.Fprintf(os.Stderr, "lina core: %v\n", err)
		if errors.Is(err, ctlfiles.ErrLocked) {
			return 69
		}
		return 78
	}
	return 0
}
