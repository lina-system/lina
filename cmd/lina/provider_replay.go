// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Resolve existing ancestors before checking Git markers, including worktrees
// and symlinked parents. Reserve a private, exclusive output before any call.
func providerOutput(path string) (*os.Root, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return nil, err
	}
	for p := parent; ; p = filepath.Dir(p) {
		if _, err = os.Lstat(filepath.Join(p, ".git")); err == nil {
			return nil, errors.New("report must be outside a repository")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(path)
	if err = root.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	return root.OpenRoot(name)
}
func readProviderInput(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 524289))
	if err != nil {
		return nil, err
	}
	if len(b) > 524288 {
		return nil, replay.ErrInvalid
	}
	return b, nil
}
func runProviderReplay(args []string) int {
	if len(args) > 0 && args[0] == "compare" {
		if len(args) != 3 {
			return 64
		}
		var reports [2]replay.ProviderReport
		for i, path := range args[1:] {
			b, err := readProviderInput(path)
			if err != nil || json.Unmarshal(b, &reports[i]) != nil {
				return developmentError("provider-replay", replay.ErrInvalid)
			}
		}
		if err := replay.Compare(os.Stdout, reports[0], reports[1]); err != nil {
			return developmentError("provider-replay", err)
		}
		return 0
	}
	flags := flag.NewFlagSet("lina provider-replay", flag.ContinueOnError)
	socket := flags.String("socket", "", "running Core socket")
	name := flags.String("provider", "", "configured provider name")
	fixture := flags.String("fixture", "", "public replay fixture JSON")
	episodes := flags.String("episode", "", "episode export JSON Lines")
	out := flags.String("out", "", "new directory outside any repository")
	model := flags.String("model", "", "override model")
	effort := flags.String("effort", "", "override effort")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if flags.NArg() != 0 || *socket == "" || *name == "" || *out == "" || (*fixture == "") == (*episodes == "") {
		flags.Usage()
		return 64
	}
	if os.Getenv("LINA_QA_REAL_MODEL") != "1" {
		fmt.Fprintln(os.Stderr, "lina provider-replay: set LINA_QA_REAL_MODEL=1 in the caller and Core to opt in.")
		return 64
	}
	p := protocol.ReplayRequest{Provider: name}
	if *model != "" {
		p.Model = model
	}
	if *effort != "" {
		p.Effort = effort
	}
	var requests []protocol.ReplayRequest
	digest := sha256.New()
	if *fixture != "" {
		b, err := readProviderInput(*fixture)
		if err != nil {
			return developmentError("provider-replay", err)
		}
		if _, err = replay.ParseProviderFixture(string(b)); err != nil {
			return developmentError("provider-replay", err)
		}
		text := string(b)
		p.Fixture = &text
		requests = append(requests, p)
		digest.Write(b)
	} else {
		f, err := os.Open(*episodes)
		if err != nil {
			return developmentError("provider-replay", err)
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 524288)
		seen := map[string]bool{}
		for scanner.Scan() {
			text := scanner.Text()
			var ep replay.Episode
			if json.Unmarshal([]byte(text), &ep) != nil || ep.Version != 1 || ep.TurnID == "" || seen[ep.TurnID] || len(requests) >= 128 {
				return developmentError("provider-replay", replay.ErrInvalid)
			}
			seen[ep.TurnID] = true
			p.Episode = &text
			requests = append(requests, p)
			digest.Write(scanner.Bytes())
			digest.Write([]byte{'\n'})
		}
		if err = scanner.Err(); err != nil || len(requests) == 0 {
			return developmentError("provider-replay", replay.ErrInvalid)
		}
	}
	dir, err := providerOutput(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina provider-replay: use a new directory outside any repository.")
		return 73
	}
	defer dir.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	c, err := developmentClient(ctx, *socket)
	if err != nil {
		return developmentError("provider-replay", err)
	}
	defer c.Close()
	report := replay.ProviderReport{Version: 1, Source: hex.EncodeToString(digest.Sum(nil)), Requests: []replay.RequestReport{}}
	save := func() error {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		return dir.WriteFile("report.json", append(b, '\n'), 0600)
	}
	fail := func(err error) int {
		report.Failure = "operation_failed"
		return developmentError("provider-replay", errors.Join(err, save()))
	}
	failed := false
	for _, request := range requests {
		if _, err = c.Replay(ctx, request); err != nil {
			return fail(err)
		}
		frame, err := c.Receive(ctx)
		if err != nil {
			return fail(err)
		}
		if frame.Error != nil {
			return fail(&clientapi.Refusal{Cause: *frame.Error})
		}
		result, ok := frame.Payload.(protocol.ReplayResult)
		if !ok {
			return fail(replay.ErrInvalid)
		}
		if result.ExcludedReason != nil {
			report.Failure = *result.ExcludedReason
			failed = true
			break
		}
		var part replay.ProviderReport
		if result.Output == nil || json.Unmarshal([]byte(*result.Output), &part) != nil || part.Version != 1 {
			return fail(replay.ErrInvalid)
		}
		report.Provider, report.Model, report.Route = part.Provider, part.Model, part.Route
		report.Requests = append(report.Requests, part.Requests...)
		report.Failure = part.Failure
		if err := save(); err != nil {
			return developmentError("provider-replay", err)
		}
		if part.Failure != "" {
			failed = true
			break
		}
	}
	if err := save(); err != nil {
		return developmentError("provider-replay", err)
	}
	if failed {
		fmt.Fprintln(os.Stderr, "Provider replay incomplete; see report.json.")
		return 1
	}
	fmt.Fprintln(os.Stdout, "Provider replay complete; see report.json.")
	return 0
}
