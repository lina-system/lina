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

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/update"
	"github.com/thisisjun786/lina/kit/provider"
)

func runCore(args []string) int {
	flags := flag.NewFlagSet("lina core", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "path to the installed local state root")
	socket := flags.String("socket", "", "path to the private client Unix socket")
	tailnet := flags.String("tailnet-listen", "", "optional exact Tailscale IP:port for remote clients")
	tailscaleSocket := flags.String("tailscale-socket", "", "trusted local tailscaled Unix socket")
	preflight := flags.Bool("preflight", false, "inspect schema and peer compatibility without starting Core")
	metadata := flags.String("manifest", "", "candidate build manifest (default: compiled rows)")
	required := flags.String("required-manifest", "", "installed peer requirements for preflight")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina core --state-root <path> (--socket <path> | --preflight) [--manifest <path>] [--required-manifest <path>] [--tailnet-listen <IP:port>] [--tailscale-socket <path>]")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		flags.Usage()
		return 64
	}
	if *root == "" || (!*preflight && *socket == "") || (*preflight && (*socket != "" || *tailnet != "" || *tailscaleSocket != "")) || (!*preflight && *required != "") || flags.NArg() != 0 || (*tailscaleSocket != "" && *tailnet == "") {
		flags.Usage()
		return 64
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if *preflight {
		result, err := update.Preflight(ctx, *root, *metadata, *required)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lina core preflight: %v\n", err)
			return 78
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return 78
		}
		return 0
	}
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
	local, err := modelpath.LocalFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina core: invalid model configuration")
		return 78
	}
	// The adapter now owns the configured credential. Consume it before boot
	// captures environments for workers, which keep their own provider setup.
	if route != nil && route.Adapter.Provider() == provider.Claude {
		if err := os.Unsetenv("CLAUDE_CODE_OAUTH_TOKEN"); err != nil {
			fmt.Fprintln(os.Stderr, "lina core: cannot isolate model credential")
			return 78
		}
	}
	cfg := boot.Config{StateRoot: *root, Socket: *socket, Conversation: route, Local: local, ClientManifest: *metadata, Work: &boot.WorkConfig{Installed: true}}
	if *tailnet != "" {
		cfg.Tailnet = &clientsrv.TailnetConfig{Address: *tailnet, Socket: *tailscaleSocket}
	}
	if err := boot.Run(ctx, cfg); err != nil {
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
