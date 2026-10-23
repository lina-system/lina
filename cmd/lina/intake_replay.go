// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
)

func runIntakeReplay(args []string) int {
	if len(args) != 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "usage: lina intake-replay <cases.jsonl>")
		if len(args) == 1 {
			return 0
		}
		return 64
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report := intakereplay.File(ctx, args[0], func(ctx context.Context, c intakereplay.Case, router string) ([]intakereplay.Binding, error) {
		deadline, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		return boot.ReplayIntakeCase(deadline, c, router)
	})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return 74
	}
	if report.Excluded > 0 {
		return 1
	}
	return 0
}
