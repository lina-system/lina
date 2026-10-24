// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

func runCompareMurmur(args []string) int {
	return compareMurmurCommand(context.Background(), args, os.Stdout, os.Stderr)
}

func compareMurmurCommand(ctx context.Context, args []string, output, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("lina compare-murmur", flag.ContinueOnError)
	// Parse diagnostics can quote arbitrary supplied values. Keep errors fixed.
	flags.SetOutput(io.Discard)
	root := flags.String("state-root", "", "LINA state root")
	inbox := flags.String("inbox", "", "copy of murmur's KakaoTalk JSONL inbox")
	person := flags.String("person", "", "source owner id")
	account := flags.String("account", "", "KakaoTalk account id")
	since := flags.String("since", "", "inclusive RFC3339 message time")
	until := flags.String("until", "", "exclusive RFC3339 message time")
	parsed := flags.Parse(args)
	start, startErr := time.Parse(time.RFC3339Nano, *since)
	end, endErr := time.Parse(time.RFC3339Nano, *until)
	if parsed != nil || flags.NArg() != 0 || *root == "" || *inbox == "" || *person == "" || *account == "" || startErr != nil || endErr != nil || !end.After(start) {
		fmt.Fprintln(diagnostic, "usage: lina compare-murmur --state-root <path> --inbox <directory> --person <id> --account <id> --since <RFC3339> --until <RFC3339>")
		return 64
	}
	db, err := canon.OpenReader(ctx, filepath.Join(*root, "canon/canon.sqlite"))
	if err != nil {
		fmt.Fprintln(diagnostic, "lina compare-murmur: cannot read Core records")
		return 74
	}
	defer db.Close(context.Background())
	result, err := messenger.Open(db, nil, nil).CompareMurmur(ctx, messenger.ComparisonRequest{Person: *person, Account: *account, Since: start, Until: end}, *inbox)
	if err != nil {
		fmt.Fprintln(diagnostic, "lina compare-murmur: comparison refused; check the source, window and stable inbox copy")
		return 65
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		fmt.Fprintln(diagnostic, "lina compare-murmur: cannot write report")
		return 74
	}
	if result.MissingLINA > 0 {
		return 1
	}
	return 0
}
