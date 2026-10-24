// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// runIdentityEval measures the sender-identity matcher. The synthetic set is in
// the repository; the owner's sample stays on the owner's machine and only its
// counts are written.
func runIdentityEval(args []string) int {
	flags := flag.NewFlagSet("lina identity-eval", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	setPath := flags.String("set", filepath.Join("testdata", "identity", "synthetic.json"), "synthetic evaluation set")
	private := flags.String("private", "", "directory holding the owner's sample (people.json, identities.json, labels.json)")
	counts := flags.String("counts", "", "where to write the private run's counts (default: the sample directory)")
	if err := flags.Parse(args); err != nil {
		return 64
	}
	ctx := context.Background()
	var set identity.Set
	var err error
	if *private != "" {
		set, err = identity.LoadOwnerSample(*private)
	} else {
		set, err = identity.LoadSet(*setPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina identity-eval: cannot read the set: %v\n", err)
		return 66
	}
	root, err := os.MkdirTemp("", "lina-identity-eval-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: cannot create a work directory")
		return 70
	}
	defer os.RemoveAll(root)
	controls, err := ctlfiles.Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: cannot open the work directory")
		return 70
	}
	defer controls.Close()
	if err := controls.Acquire(); err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: cannot lock the work directory")
		return 70
	}
	epoch, err := controls.MintEpoch(0, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: cannot mint an epoch")
		return 70
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: cannot open a working store")
		return 70
	}
	defer db.Close(context.Background())
	removals, err := retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: cannot open the removal list")
		return 70
	}
	defer removals.Close()
	measures, err := identity.Evaluate(ctx, identity.Open(db, "owner", nil, removals), set)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina identity-eval: the run failed")
		return 70
	}
	passed := measures.Gate()
	report := identity.Report(measures)
	if *private != "" {
		passed = measures.OwnerGate()
		report = identity.ReportOwner(measures)
	}
	fmt.Println(report)
	if *private != "" {
		target := *counts
		if target == "" {
			target = filepath.Join(*private, "counts.json")
		}
		if err := identity.WriteCounts(target, measures); err != nil {
			fmt.Fprintln(os.Stderr, "lina identity-eval: cannot write the counts")
			return 70
		}
		fmt.Printf("counts: %s\n", target)
	}
	if !passed {
		return 1
	}
	return 0
}
