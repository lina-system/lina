// SPDX-License-Identifier: Apache-2.0
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
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/addons/importer/murmur"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

func runCompareMurmur(args []string) int {
	return compareMurmurCommand(args, os.Stdout, os.Stderr)
}

func compareMurmurCommand(args []string, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("lina compare-murmur", flag.ContinueOnError)
	// Flag errors can echo arbitrary input, so report a fixed diagnostic instead.
	flags.SetOutput(io.Discard)
	root := flags.String("state-root", "", "installed state root; Core may keep running")
	inbox := flags.String("inbox", "", "handed-over murmur version-1 spool JSONL")
	person := flags.String("person", "", "person id")
	platform := flags.String("platform", "kakaotalk", "kakaotalk, discord or imessage")
	account := flags.String("account", "", "source account id")
	since := flags.String("since", "", "inclusive RFC3339 message time")
	until := flags.String("until", "", "exclusive RFC3339 message time")
	usage := func() {
		fmt.Fprintln(diagnostics, "usage: lina compare-murmur --state-root <path> --inbox <path> --person <id> --account <id> --since <time> --until <time> [--platform kakaotalk|discord|imessage]")
	}
	if err := flags.Parse(args); err != nil {
		usage()
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	from, err := time.Parse(time.RFC3339Nano, *since)
	to, untilErr := time.Parse(time.RFC3339Nano, *until)
	if *root == "" || *inbox == "" || *person == "" || *account == "" || flags.NArg() != 0 || err != nil || untilErr != nil || from.IsZero() || !to.After(from) || (*platform != "kakaotalk" && *platform != "discord" && *platform != "imessage") {
		usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	result, err := compareMurmur(ctx, *root, *inbox, messenger.ComparisonRequest{Person: *person, Platform: *platform, Account: *account, Since: from, Until: to})
	if err != nil {
		fmt.Fprintln(diagnostics, "lina compare-murmur: comparison unavailable; check the source, account and complete version-1 inbox")
		return 78
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return 74
	}
	return 0
}

func compareMurmur(ctx context.Context, root, path string, q messenger.ComparisonRequest) (messenger.Comparison, error) {
	file, err := os.Open(path)
	if err != nil {
		return messenger.Comparison{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return messenger.Comparison{}, err
	}
	if !before.Mode().IsRegular() {
		return messenger.Comparison{}, messenger.ErrInvalid
	}
	// A bounded snapshot excludes later appends; a changed file refuses the
	// entire comparison instead of reporting a misleading partial success.
	keys, err := murmur.ComparisonRecords(ctx, io.LimitReader(file, before.Size()), q.Platform, q.Account)
	if err != nil {
		return messenger.Comparison{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return messenger.Comparison{}, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return messenger.Comparison{}, messenger.ErrInvalid
	}
	db, err := canon.OpenReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return messenger.Comparison{}, err
	}
	defer db.Close(context.Background())
	return messenger.Open(db, nil, nil).Compare(ctx, q, keys)
}
