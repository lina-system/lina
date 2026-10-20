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
)

func runCore(args []string) int {
	flags := flag.NewFlagSet("lina core", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "path to the private client Unix socket")
	flags.Usage = func() { fmt.Fprintln(os.Stderr, "usage: lina core --socket <path>") }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		flags.Usage()
		return 64
	}
	if *socket == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := boot.Run(ctx, *socket); err != nil {
		fmt.Fprintf(os.Stderr, "lina core: %v\n", err)
		return 78
	}
	return 0
}
