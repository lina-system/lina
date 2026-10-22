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
	"syscall"

	"github.com/thisisjun786/lina/internal/core/update"
)

func runUpdate(args []string) int {
	flags := flag.NewFlagSet("lina update", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	prepare := flags.Bool("prepare", false, "verify and prepare a candidate without activating it")
	root := flags.String("state-root", "", "installed state root")
	archive := flags.String("archive", "", "local release tar.gz archive")
	digest := flags.String("sha256", "", "expected SHA-256 from release metadata")
	required := flags.String("required-manifest", "", "installed peer requirements (default: installed manifest)")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina update --prepare --state-root <path> --archive <tar.gz> --sha256 <digest> [--required-manifest <path>]")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		flags.Usage()
		return 64
	}
	if !*prepare || *root == "" || *archive == "" || *digest == "" || flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	prepared, err := update.Prepare(ctx, update.Config{StateRoot: *root, Archive: *archive, SHA256: *digest, RequiredManifest: *required})
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina update: %v\n", err)
		return 78
	}
	if err := json.NewEncoder(os.Stdout).Encode(prepared); err != nil {
		return 78
	}
	return 0
}
