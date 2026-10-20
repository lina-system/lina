// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/internal/buildinfo"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/ui"
	"github.com/thisisjun786/lina/kit/protocol"
)

func runTUI(args []string) int {
	flags := flag.NewFlagSet("lina tui", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "path to the Core client Unix socket")
	flags.Usage = func() { fmt.Fprintln(os.Stderr, "usage: lina tui --socket <path>") }
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
	if err := ui.Run(context.Background(), *socket, buildinfo.Version, os.Stdin, os.Stdout); err != nil {
		var refusal *clientapi.Refusal
		switch {
		case errors.Is(err, clientapi.ErrUnavailable):
			fmt.Fprintln(os.Stderr, "lina tui: unavailable; start Core and check --socket")
			return 69
		case errors.As(err, &refusal):
			fmt.Fprintf(os.Stderr, "lina tui: %s; check Core compatibility\n", refusal)
			if refusal.Cause.Code == protocol.ErrorUnsupportedCombination {
				return 78
			}
			return 70
		default:
			// Peer text and local paths are not safe terminal status strings.
			fmt.Fprintln(os.Stderr, "lina tui: connection or input/output failed; check Core and terminal input")
			return 70
		}
	}
	return 0
}
