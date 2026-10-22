// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (g githubService) mergeRequest(ctx context.Context, s clientsrv.Session, p protocol.WorkRequest) (protocol.WorkResult, error) {
	if g.merges == nil {
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	op, id := *p.Operation, *p.BindingID
	if op == "github_bind" {
		if g.agentExists == nil || !g.agentExists(ctx, *p.AgentID) {
			return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
		}
		refs, err := g.client.ProtectedRefs(ctx, *p.Clone, *p.Repository, *p.ProtectedRefs)
		if err != nil {
			return mergeWire(id, ghclient.MergeResult{State: "blocked", Cause: mergeCause(err)}), nil
		}
		n, err := strconv.Atoi(*p.PrNumber)
		if err != nil {
			return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		b := authority.MergeBinding{ID: id, Person: s.PersonID, Agent: *p.AgentID, Repository: strings.ToLower(*p.Repository), Number: n, Base: *p.Base, Clone: *p.Clone, Protected: refs}
		if err = g.merges.Bind(ctx, b); err != nil {
			return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
		}
		stored, e := g.merges.Get(ctx, id, s.PersonID)
		if e != nil {
			return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
		}
		result := ghclient.MergeResult{State: stored.State, Head: stored.Head, Base: stored.Base}
		if len(stored.Result) > 0 {
			if e = json.Unmarshal(stored.Result, &result); e != nil {
				return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
			}
			result.State = stored.State
		}
		return mergeWire(id, result), nil
	}
	b, err := g.merges.Get(ctx, id, s.PersonID)
	if err != nil {
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	r := ghclient.MergeResult{State: b.State, Head: b.Head, Base: b.Base, CI: "pending"}
	if len(b.Result) > 0 {
		if err = json.Unmarshal(b.Result, &r); err != nil {
			return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
		}
		r.State = b.State
	}
	if op == "github_merge_status" || b.State == "merged" {
		return mergeWire(id, r), nil
	}
	ref := ghclient.Ref{Repository: b.Repository, Number: b.Number}
	checkRefs := func(ctx context.Context) (map[string]string, error) {
		current, err := g.client.ProtectedRefs(ctx, b.Clone, b.Repository, slices.Sorted(maps.Keys(b.Protected)))
		if err == nil && !maps.Equal(current, b.Protected) {
			err = authority.ErrProtectedRef
		}
		return current, err
	}
	switch op {
	case "github_accept":
		if b.State == "unknown" || b.State == "queued" {
			r.Cause = "merge_outcome_unknown"
			return mergeWire(id, r), nil
		}
		snapshot, e := g.client.Read(ctx, ref)
		r.Head = snapshot.Head
		r.CI = snapshot.CI
		r.Checks = snapshot.Checks
		if e != nil {
			r.Cause = snapshot.Cause
			break
		}
		if snapshot.Head != *p.Head {
			r.Cause = "head_changed"
			break
		}
		if snapshot.State != "OPEN" {
			r.Cause = "pr_not_open"
			break
		}
		if _, err = checkRefs(ctx); err != nil {
			r.Cause = mergeCause(err)
			break
		}
		if err = g.merges.Accept(ctx, id, s.PersonID, snapshot.Head); err != nil {
			return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
		}
		r.State = "ready"
		r.Cause = ""
		return mergeWire(id, r), nil
	case "github_merge":
		if b.State == "unknown" || b.State == "queued" {
			r.Cause = "merge_outcome_unknown"
			return mergeWire(id, r), nil
		}
		if b.State != "ready" {
			r.Cause = "head_not_accepted"
			break
		}
		r, err = g.client.Merge(ctx, ref, b.Base, b.Head, func(ctx context.Context) error {
			refs, e := checkRefs(ctx)
			if e != nil {
				return e
			}
			return g.merges.Begin(ctx, id, s.PersonID, b.Head, refs)
		})
		if err != nil && r.Cause == "" {
			r.Cause = mergeCause(err)
		}
	case "github_observe_merge":
		r, err = g.client.ObserveMerge(ctx, ref, b.Base)
		if err != nil && r.Cause == "" {
			r.Cause = mergeCause(err)
		}
	default:
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	if r.State != "merged" && r.State != "queued" && r.State != "unknown" {
		r.State = "blocked"
	}
	// Use the live caller context: if persistence is interrupted the already
	// committed unknown intent remains reserved, never retried automatically.
	raw, _ := json.Marshal(r)
	if err = g.merges.Record(ctx, id, s.PersonID, r.State, raw); err != nil {
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	stored, err := g.merges.Get(ctx, id, s.PersonID)
	if err != nil {
		return protocol.WorkResult{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	r.State = stored.State
	return mergeWire(id, r), nil
}
func mergeCause(err error) string {
	if errors.Is(err, authority.ErrProtectedRef) {
		return "protected_ref_changed"
	}
	// These are adapter-owned codes; raw child output is never returned.
	for _, code := range []string{"base_changed", "merge_commit_missing", "merge_commit_mismatch", "target_ref_mismatch", "invalid_merge_evidence", "clone_repository_mismatch", "protected_ref_unreadable", "git_unavailable", "invalid_protected_refs"} {
		if err.Error() == code {
			return code
		}
	}
	return "merge_evidence_unavailable"
}
func mergeWire(id string, r ghclient.MergeResult) protocol.WorkResult {
	checks := []protocol.GithubCheck{}
	for _, c := range r.Checks {
		checks = append(checks, protocol.GithubCheck{Name: c.Name, State: c.State, Conclusion: c.Conclusion})
	}
	done := r.State == "merged"
	if r.CI == "" {
		r.CI = "pending"
	}
	return protocol.WorkResult{BindingID: &id, Head: &r.Head, Base: &r.Base, State: &r.State, Cause: &r.Cause, MergeCommit: &r.Commit, Source: &r.Source, Ci: &r.CI, Checks: &checks, Done: &done}
}
