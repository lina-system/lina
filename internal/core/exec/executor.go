// SPDX-License-Identifier: Apache-2.0

// Package exec composes validated tools, authority admission and sandboxed work.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/sandbox"
	"github.com/thisisjun786/lina/kit/tools"
)

// Config comes from Core's installed paths and grants, never tool arguments.
// Versions is a dedicated private directory, separate from Root and from every
// protected Core tree. Commands cannot access it; only the file worker can.
type Config struct {
	Scope, Root, Versions, Lina        string
	ReadDenied, Env, ProviderVariables []string
}
type Executor struct {
	cfg             Config
	files, commands *sandbox.Sandbox
	slot            chan struct{}
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func New(owner *proc.Owner, cfg Config) (*Executor, error) {
	if owner == nil || cfg.Scope == "" || !filepath.IsAbs(cfg.Lina) || !filepath.IsAbs(cfg.Root) || !filepath.IsAbs(cfg.Versions) {
		return nil, tools.ErrInvalid
	}
	var err error
	if cfg.Root, err = filepath.EvalSymlinks(cfg.Root); err != nil {
		return nil, err
	}
	if cfg.Versions, err = filepath.EvalSymlinks(cfg.Versions); err != nil {
		return nil, err
	}
	if overlaps(cfg.Root, cfg.Versions) {
		return nil, tools.ErrInvalid
	}
	info, err := os.Stat(cfg.Versions)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, tools.ErrInvalid
	}
	base := sandbox.Config{WorkDir: cfg.Root, ReadDenied: append([]string{}, cfg.ReadDenied...), Env: cfg.Env, ProviderVariables: cfg.ProviderVariables}
	fileConfig := base
	fileConfig.WriteRoots = []string{cfg.Versions}
	commandConfig := base
	commandConfig.ReadDenied = append(append([]string{}, base.ReadDenied...), cfg.Versions)
	return &Executor{cfg: cfg, files: sandbox.New(owner, fileConfig), commands: sandbox.New(owner, commandConfig), slot: make(chan struct{}, 1)}, nil
}

// One executor owns each workspace. The shared slot spans all file operations
// and commands; a cancelled waiter never executes later. Registry deadlines do
// not span the person's approval wait, because admission precedes the handler.
func (e *Executor) lock(ctx context.Context) error {
	select {
	case e.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-e.slot
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *Executor) RegisterFiles(registry *tools.Registry) error {
	if registry == nil {
		return tools.ErrInvalid
	}
	for _, name := range []string{"read_file", "write_file", "delete_file", "restore_file", "apply_patch"} {
		spec, err := tools.FileSpec(name, e.cfg.Scope)
		if err != nil {
			return err
		}
		if err := registry.Register(spec, e.file); err != nil {
			return err
		}
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if len(data) > b.limit-b.Len() {
		data = data[:b.limit-b.Len()]
		b.overflow = true
	}
	b.Buffer.Write(data)
	return n, nil
}
func resultCode(state tools.State, code string) tools.Result {
	body, _ := json.Marshal(map[string]string{"code": code})
	return tools.Result{State: state, Body: body}
}
func (e *Executor) file(ctx context.Context, l lease.Lease, in tools.Input) (tools.Result, error) {
	if err := e.lock(ctx); err != nil {
		return resultCode(tools.Cancelled, "cancelled"), err
	}
	defer func() { <-e.slot }()
	reply, err := e.fileRequest(ctx, l, in.Arguments)
	if err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			return resultCode(tools.Failed, "sandbox_unavailable"), err
		}
		return resultCode(tools.Unknown, "effect_unknown"), err
	}
	return fileResult(reply)
}

func fileResult(reply tools.FileReply) (tools.Result, error) {
	body, _ := json.Marshal(reply)
	result := tools.Result{State: reply.State, Body: body, Revision: reply.Result.Revision}
	switch reply.State {
	case tools.Applied:
		if reply.Code != "" || reply.Result.Revision == "" {
			return resultCode(tools.Unknown, "effect_unknown"), tools.ErrUnknown
		}
		return result, nil
	case tools.Failed, tools.Cancelled:
		if reply.Code == "" {
			return resultCode(tools.Unknown, "effect_unknown"), tools.ErrUnknown
		}
		return result, errors.New(reply.Code)
	case tools.Unknown:
		// Unknown is not success, but its retained versions are the only safe
		// recovery path after a partial multi-file commit. Preserve the reply.
		if reply.Code != "effect_unknown" {
			return resultCode(tools.Unknown, "effect_unknown"), tools.ErrUnknown
		}
		return result, tools.ErrUnknown
	case tools.None, tools.Refused:
		return resultCode(tools.Unknown, "effect_unknown"), tools.ErrUnknown
	default:
		return resultCode(tools.Unknown, "effect_unknown"), tools.ErrUnknown
	}
}
func (e *Executor) fileRequest(ctx context.Context, l lease.Lease, raw json.RawMessage) (tools.FileReply, error) {
	var reply tools.FileReply
	out := boundedBuffer{limit: 3 * tools.MaxFileBytes}
	err := e.files.Run(ctx, l, sandbox.Command{Path: e.cfg.Lina, Args: []string{"file-tool", "--root", e.cfg.Root, "--versions", e.cfg.Versions}, Stdin: bytes.NewReader(raw), Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return reply, err
	}
	if out.overflow {
		return reply, tools.ErrUnknown
	}
	d := json.NewDecoder(&out)
	d.DisallowUnknownFields()
	if err := d.Decode(&reply); err != nil {
		return reply, tools.ErrUnknown
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return reply, tools.ErrUnknown
	}
	return reply, nil
}
