// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/sandbox"
	"github.com/thisisjun786/lina/kit/tools"
)

type CommandResult struct {
	Stdout    string              `json:"stdout"`
	Stderr    string              `json:"stderr"`
	ExitCode  int                 `json:"exit_code"`
	Truncated bool                `json:"truncated"`
	Changes   []tools.FileReceipt `json:"changes,omitempty"`
}

// RegisterCommand installs a Core-selected command prefix. Every command needs
// a write grant for the workspace: even an apparent read may write caches or
// follow command-line options. The sandbox still denies connected-source writes.
func (e *Executor) RegisterCommand(registry *tools.Registry, policy tools.CommandPolicy) (tools.Spec, error) {
	if registry == nil || policy.Target != e.cfg.Scope || len(policy.Prefix) == 0 || !filepath.IsAbs(policy.Prefix[0]) {
		return tools.Spec{}, tools.ErrInvalid
	}
	policy.Write = true
	spec, err := tools.CommandSpec(policy)
	if err != nil {
		return tools.Spec{}, err
	}
	err = registry.Register(spec, e.command)
	return spec, err
}

// Snapshot provides the trusted workspace observation used by grant admission.
// It reads through the same sandboxed worker and takes an activity lease.
func (e *Executor) Snapshot(ctx context.Context, l lease.Lease) (tools.FileResult, error) {
	if err := e.lock(ctx); err != nil {
		return tools.FileResult{}, err
	}
	defer func() { <-e.slot }()
	return e.snapshot(ctx, l)
}
func (e *Executor) snapshot(ctx context.Context, l lease.Lease) (tools.FileResult, error) {
	reply, err := e.fileRequest(ctx, l, json.RawMessage(`{"operation":"manifest"}`))
	if err != nil {
		return tools.FileResult{}, err
	}
	if reply.State != tools.Applied || reply.Result.Revision == "" {
		return tools.FileResult{}, tools.ErrUnknown
	}
	return reply.Result, nil
}
func (e *Executor) command(ctx context.Context, l lease.Lease, in tools.Input) (tools.Result, error) {
	if err := e.lock(ctx); err != nil {
		return resultCode(tools.Cancelled, "cancelled"), err
	}
	defer func() { <-e.slot }()
	var request tools.CommandRequest
	if err := json.Unmarshal(in.Arguments, &request); err != nil || len(request.Argv) == 0 {
		return resultCode(tools.Failed, "invalid_tool_call"), tools.ErrInvalid
	}
	before, err := e.snapshot(ctx, l)
	if err != nil {
		return resultCode(tools.Failed, "file_manifest_unavailable"), err
	}
	out, diagnostic := boundedBuffer{limit: 1 << 20}, boundedBuffer{limit: 1 << 20}
	runErr := e.commands.Run(ctx, l, sandbox.Command{Path: request.Argv[0], Args: request.Argv[1:], Network: request.Network, Stdout: &out, Stderr: &diagnostic})
	if errors.Is(runErr, sandbox.ErrUnavailable) {
		return resultCode(tools.Failed, "sandbox_unavailable"), runErr
	}
	// The process owner has reaped the command and killed its descendants. A
	// bounded cleanup snapshot records its writes even after cancellation.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	after, snapshotErr := e.snapshot(cleanup, l)
	result := tools.Result{State: tools.Applied, Revision: after.Revision}
	receipt := CommandResult{Stdout: out.String(), Stderr: diagnostic.String(), Truncated: out.overflow || diagnostic.overflow, Changes: tools.ManifestDiff(before.Manifest, after.Manifest)}
	if snapshotErr != nil {
		result.State = tools.Unknown
		receipt.Changes = nil
	}
	if runErr != nil {
		var exit interface{ ExitCode() int }
		if errors.As(runErr, &exit) && ctx.Err() == nil && snapshotErr == nil {
			receipt.ExitCode = exit.ExitCode()
			result.State = tools.Failed
			if len(receipt.Changes) != 0 {
				// A failed process may have partially applied its effect. Keep
				// the observed revision/receipts and the authority write fence
				// until reconciliation, rather than claim no effect occurred.
				result.State = tools.Unknown
			}
		} else {
			receipt.ExitCode = -1
			result.State = tools.Unknown
		}
	}
	result.Body, _ = json.Marshal(receipt)
	return result, errors.Join(runErr, snapshotErr)
}
