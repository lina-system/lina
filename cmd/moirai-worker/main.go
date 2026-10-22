// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/moirai"
	"github.com/thisisjun786/lina/internal/moirai/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func main() {
	// Service configuration is explicit. Inherited provider/account variables are
	// discarded before opening canon or connecting to Core.
	os.Clearenv()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}
func run(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("moirai-worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("canon", "", "existing canon database")
	socket := flags.String("socket", "", "Core job socket")
	preflight := flags.Bool("preflight", false, "check canon and Core job readiness without running jobs")
	once := flags.Bool("once", false, "run each currently due job once")
	interval := flags.Duration("poll", time.Second, "job polling interval")
	if err := flags.Parse(args); err != nil {
		return 64
	}
	if *path == "" || *socket == "" || *interval <= 0 || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: moirai-worker --canon <path> --socket <path> [--once] [--poll <duration>]")
		return 64
	}
	reader, err := canon.Open(ctx, *path)
	if err != nil {
		fmt.Fprintln(stderr, "canon_unavailable")
		return 69
	}
	defer reader.Close()
	if *preflight {
		if _, err = reader.Ready(ctx, time.Now()); err == nil {
			err = jobapi.NewClient(*socket).Ready(ctx)
		}
		if err != nil {
			fmt.Fprintln(stderr, "job_unavailable")
			return 69
		}
		return 0
	}
	worker := moirai.Worker{Reader: reader, Core: jobapi.NewClient(*socket)}
	if *once {
		err = worker.Once(ctx)
	} else {
		err = worker.Run(ctx, *interval)
	}
	if err != nil {
		fmt.Fprintln(stderr, "job_unavailable")
		return 69
	}
	return 0
}
