// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/core/install"
)

func runInstall(args []string) int {
	host := install.NewProcessHost()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		host.Close(ctx)
	}()
	return runInstallWithHost(args, host)
}

func runInstallWithHost(args []string, host install.Host) int {
	flags := flag.NewFlagSet("lina install", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("state-root", "", "new private local state root (default: XDG_STATE_HOME/lina)")
	units := flags.String("unit-dir", "", "systemd user unit directory (default: XDG_CONFIG_HOME/systemd/user)")
	bundle := flags.String("bundle", "", "verified build bundle (default: the executable's directory)")
	socket := flags.String("socket", "", "private client socket (default: <state-root>/system/run/client.sock)")
	personaSource := flags.String("persona-source", "", "local persona checkout root (absolute path)")
	paths := map[string]string{}
	flags.Func("tool", "installed tool override, name=/absolute/path (repeatable)", func(value string) error {
		name, path, ok := strings.Cut(value, "=")
		if !ok || name == "" || !filepath.IsAbs(path) {
			return errors.New("tool requires name=/absolute/path")
		}
		if _, exists := paths[name]; exists {
			return errors.New("tool override is duplicated")
		}
		paths[name] = path
		return nil
	})
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina install [--state-root <path>] [--bundle <path>] [--unit-dir <path>] [--socket <path>] [--persona-source <dir>] [--tool name=<path>]")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina install: cannot determine the user's home directory")
		return 78
	}
	if *root == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local/state")
		}
		*root = filepath.Join(base, "lina")
	}
	if *units == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		*units = filepath.Join(base, "systemd/user")
	}
	if *bundle == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lina install: cannot locate the build bundle")
			return 78
		}
		*bundle = filepath.Dir(exe)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result, err := install.Run(ctx, install.Config{StateRoot: *root, UnitDir: *units, BundleDir: *bundle, Socket: *socket, ToolPaths: paths, PersonaSource: *personaSource}, host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lina install: %v\n", err)
		return 78
	}
	fmt.Fprintf(os.Stdout, "Installed LINA Core. Client socket: %s\n", result.Socket)
	for _, tool := range result.Tools {
		switch tool.Status {
		case "unverified":
			fmt.Fprintf(os.Stdout, "%s: %s (unverified; verified versions: %s)\n", tool.Name, tool.Version, verifiedText(tool.VerifiedVersions))
		case "verified":
			fmt.Fprintf(os.Stdout, "%s: %s (verified)\n", tool.Name, tool.Version)
		default:
			fmt.Fprintf(os.Stdout, "%s: %s\n", tool.Name, tool.Status)
		}
	}
	for _, cause := range result.Unavailable {
		fmt.Fprintln(os.Stdout, cause)
	}
	fmt.Fprintln(os.Stdout, "No account was signed in. Continue with ChatGPT when you open a conversation.")
	return 0
}

func verifiedText(versions []string) string {
	if len(versions) == 0 {
		return "none"
	}
	return strings.Join(versions, ", ")
}
