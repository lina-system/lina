// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/sandbox"
)

func startSandbox(ctx context.Context, root, socket string) (*sandbox.Sandbox, func(context.Context) error) {
	owner := proc.NewOwner()
	workspace := filepath.Join(root, "workspace")
	// Failure is reported by the probe as invalid_policy; it never enables an
	// unsandboxed fallback or prevents the client service from running.
	_ = os.Mkdir(workspace, 0700)
	denied := []string{socket}
	for _, name := range []string{"canon", "secrets", "system", "versions", "backups"} {
		path := filepath.Join(root, name)
		_ = os.Mkdir(path, 0700)
		denied = append(denied, path)
	}
	box := sandbox.New(owner, sandbox.Config{WorkDir: workspace, ReadDenied: denied})
	l := lease.New(func() {})
	status := box.Probe(ctx, l)
	l.Release()
	if !status.Available {
		fmt.Fprintf(os.Stderr, "shell_file_tools_unsupported: %s\n", status.Cause)
	}
	return box, owner.Close
}
