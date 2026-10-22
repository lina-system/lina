// SPDX-License-Identifier: Apache-2.0
package install

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// ProcessHost uses kit/proc for every installer command. No shell interprets
// installer arguments, and inherited provider credentials are filtered there.
type ProcessHost struct {
	owner    *proc.Owner
	env      []string
	activity *lifecycle.ActivityRegistry
}

func NewProcessHost() *ProcessHost {
	return &ProcessHost{owner: proc.NewOwner(), env: proc.InheritedEnvironment("LINA_RESPONSES_API_KEY"), activity: lifecycle.NewRegistry(nil)}
}
func (h *ProcessHost) Close(ctx context.Context) error { return h.owner.Close(ctx) }
func (h *ProcessHost) LookPath(name string) (string, error) {
	if filepath.Base(name) != name {
		return "", errors.New("tool name must be a basename")
	}
	var search string
	for _, entry := range h.env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			search = value
		}
	}
	for _, dir := range filepath.SplitList(search) {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}
func (h *ProcessHost) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return h.RunWithContext(ctx, path, args...)
}

// RunWithContext uses only the caller's cancellation and deadline. Backup and
// restore scale with installed data and must not inherit the installer probe
// timeout. Process ownership, environment filtering and output bounds still apply.
func (h *ProcessHost) RunWithContext(ctx context.Context, path string, args ...string) ([]byte, error) {
	l, err := h.activity.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "installation command"})
	if err != nil {
		return nil, err
	}
	defer l.Release()
	var output boundedOutput
	err = h.owner.Run(ctx, l, proc.Command{Path: path, Args: args, Env: h.env, Stdout: &output, Stderr: io.Discard})
	if output.overflow {
		err = errors.Join(err, errors.New("command output exceeds 4096 bytes"))
	}
	return output.buffer.Bytes(), err
}

type boundedOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - b.buffer.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
