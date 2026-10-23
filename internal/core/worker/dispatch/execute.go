// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

func (s *Service) execute(r Record) {
	// Shutdown does not replay a possibly effective dispatch. Recovery marks the
	// running record unknown and retains the task's active reservation.
	ctx := s.ctx
	admitted, settled := false, false
	if r.Attempt == 0 {
		r.Attempt = 1
	}
	defer func() { s.mu.Lock(); delete(s.running, r.ID); s.mu.Unlock() }()
	defer func() {
		if ctx.Err() == nil && !settled {
			r.State = "finished"
			if admitted {
				r.State = "unknown"
			}
			_ = s.save(ctx, r)
		}
	}()
	r.Code = "start_failed"
	if s.cfg.Work == nil || s.cfg.Area == nil {
		return
	}
	// Queued records from before shared allocation have no assigned branch.
	// Persist the assignment before creating any workspace or worker process.
	if r.Branch == "" {
		r.Branch = taskBranch(r.Definition.Branch, r.ID)
		if err := s.save(ctx, r); err != nil {
			return
		}
	}
	if r.Owner != "" {
		var live int
		err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE id=? AND preset='dev' AND trashed_at=0", r.Owner).Scan(&live)
		})
		if err != nil || live != 1 {
			r.Code = "dev_agent_unavailable"
			return
		}
		if err = authority.CheckPreset(ctx, s.cfg.DB, s.cfg.Epoch, authority.PolicyRequest{Key: "preset:worker:" + r.ID, Person: r.Origin.Person, Agent: r.Owner, TaskID: "coding:" + r.ID, Tool: "dispatch_work", Target: r.Definition.Repository, Class: tools.WorkerEffect, Arguments: mustJSON(r.Definition)}); err != nil {
			r.Code = "preset_effect_limit"
			return
		}
	}
	l, err := s.cfg.Activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "delegated worker task"})
	if err != nil {
		return
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	git := func(dir string, args ...string) (string, error) { return s.command(ctx, l, owner, dir, args...) }
	d := r.TaskDefinition()
	repo, err := filepath.EvalSymlinks(d.Repository)
	if err != nil {
		return
	}
	remote, err := git(repo, "remote", "get-url", "origin")
	if err != nil {
		return
	}
	if !strings.HasPrefix(remote, "https://github.com/") && !strings.HasPrefix(remote, "git@github.com:") {
		r.Code = "repository_unsupported"
		return
	}
	repository := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(remote, "https://github.com/"), "git@github.com:"), ".git")
	if strings.Count(repository, "/") != 1 || strings.ContainsAny(repository, ": \t\n") {
		r.Code = "repository_unsupported"
		return
	}
	for _, ref := range []string{d.Base, d.Branch} {
		if _, err = git(repo, "check-ref-format", "--branch", ref); err != nil {
			r.Code = "invalid_branch"
			return
		}
	}
	base, err := git(repo, "rev-parse", "--verify", d.Base+"^{commit}")
	if err != nil {
		return
	}
	if r.BaseCommit != "" {
		base = r.BaseCommit
	} else {
		r.BaseCommit = base
	}
	protectedArgs := []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes", "refs/lina"}
	protected, err := git(repo, protectedArgs...)
	if err != nil {
		return
	}
	for _, ref := range d.DesignRefs {
		file, anchor, _ := strings.Cut(ref, "#")
		body, err := git(repo, "show", base+":"+file)
		if err != nil || !hasHeading(body, anchor) {
			r.Code = "design_reference_missing"
			return
		}
	}
	workdir := filepath.Join(s.cfg.Root, "work", "tasks", r.ID)
	// Keep adapter evidence outside the worker's checkout so observing a result
	// neither dirties its commit nor overwrites a file it produced.
	taskRoot := filepath.Join(s.cfg.Root, "work", "runs", r.ID)
	workspace := work.Workspace{TaskID: r.ID, Path: workdir, Spec: work.WorkspaceSpec{
		RepositoryID: fmt.Sprintf("%x", sha256.Sum256([]byte(repo))), Source: repo, Base: base, Branch: d.Branch,
	}}
	service := s.cfg.Work
	ownerAgent, manager := r.Owner, r.Manager
	if ownerAgent == "" {
		ownerAgent = r.Origin.Agent
		manager = r.Origin.Conversation
	}
	if err = service.Create(ctx, work.Task{ID: r.ID, PersonID: r.Origin.Person, AgentID: ownerAgent, ConversationID: manager, Title: d.What, Done: d.Done, ArtifactRoots: []string{workdir, taskRoot}, Workspace: &workspace, RequiresApply: true}); err != nil {
		return
	}
	if _, err = s.cfg.Area.Prepare(ctx, l, r.ID, workspace.Spec); err != nil {
		return
	}
	if _, err = git(workdir, "remote", "set-url", "origin", remote); err != nil {
		return
	}
	if err = os.MkdirAll(taskRoot, 0700); err != nil {
		return
	}
	protectedWorkRefs := func() (string, error) {
		refs, err := git(workdir, protectedArgs...)
		var protected []string
		for _, ref := range strings.Split(refs, "\n") {
			if !strings.HasPrefix(ref, "refs/heads/"+d.Branch+" ") {
				protected = append(protected, ref)
			}
		}
		return strings.Join(protected, "\n"), err
	}
	workProtected, err := protectedWorkRefs()
	if err != nil {
		return
	}
	adapter, observation, err := s.attempt(r.WorkerName())
	if err != nil {
		r.Code = "worker_unavailable"
		return
	}
	defer adapter.Close(context.Background())
	r.Version, err = adapter.Version(ctx)
	if err != nil {
		return
	}
	packet, err := taskPacket(r)
	if err != nil {
		return
	}
	// Commit the launch intent before the child can have any effect.
	if err = s.save(ctx, r); err != nil {
		return
	}
	admitted = true
	attemptID := fmt.Sprintf("%s:%d", r.ID, r.Attempt)
	thread, err := adapter.Start(ctx, l, worker.StartSpec{TaskID: r.ID, AttemptID: attemptID, Workdir: workdir, TaskPacket: packet})
	if err != nil {
		return
	}
	run := work.Run{ID: attemptID, TaskID: r.ID, Generation: r.Attempt, Worker: adapter.Name(), ThreadID: thread.ID, TurnID: thread.TurnID, NodeID: s.cfg.Main}
	if err = service.Start(ctx, run); err != nil {
		return
	}
	admitted = true
	var finish worker.Finish
	waiting := true
	for waiting {
		select {
		case event := <-adapter.Events():
			if event.Thread != thread || event.Validate() != nil {
				r.Code = "worker_protocol_error"
				return
			}
			if event.Finish != nil {
				finish = *event.Finish
				waiting = false
			}
		case <-ctx.Done():
			return
		}
	}
	// End the task process tree even if it kept stdout open after its result.
	if err = adapter.Close(ctx); err != nil {
		return
	}
	snapshot, snapshotErr := adapter.Snapshot(ctx, thread)
	if snapshotErr == nil {
		// Retain only typed evidence, not untrusted output or pending request bodies.
		r.Snapshot = &worker.ThreadSnapshot{Thread: snapshot.Thread, State: snapshot.State, Finish: snapshot.Finish, EffectsKnown: snapshot.EffectsKnown && len(snapshot.Pending) == 0}
		if err = s.save(ctx, r); err != nil {
			return
		}
	}
	r.State = "finished"
	rawResult, denied := observation()
	observed, err := secrets.Mask(string(mustJSON(struct {
		Result json.RawMessage            `json:"result"`
		Denied []antigravity.DeniedAction `json:"denied_actions"`
	}{rawResult, denied})))
	if err != nil {
		r.Code = "invalid_result"
		return
	}
	// Mask before assigning to the record: the deferred journal save must not
	// retain the unmasked final response even if later PR reads fail.
	if err = json.Unmarshal([]byte(observed.Text()), &r); err != nil {
		r.Code = "invalid_result"
		return
	}
	status, outcome := "completed", "ready_for_review"
	if finish.Status != worker.Done {
		status, outcome = "failed", "failed"
		r.Code = finish.Code
	} else {
		r.Code = "pr_unverified"
		after, e := git(repo, protectedArgs...)
		workAfter, workErr := protectedWorkRefs()
		head, headErr := git(workdir, "rev-parse", "HEAD")
		branch, branchErr := git(workdir, "branch", "--show-current")
		dirty, dirtyErr := git(workdir, "status", "--porcelain")
		files, filesErr := git(workdir, "diff", "--name-only", base+".."+head)
		if e == nil && after == protected && workErr == nil && workAfter == workProtected && headErr == nil && head != base && branchErr == nil && branch == d.Branch && dirtyErr == nil && dirty == "" && filesErr == nil && allowedFiles(files, d.WriteArea) {
			pr, prErr := antigravity.ReadPullRequest(ctx, s.cfg.PR, repository, d.Branch, d.Base, head)
			if prErr == nil {
				r.PR = &pr
				r.Code = "change_evidence_unavailable"
				patch, patchErr := git(workdir, "diff", "--no-ext-diff", "--no-textconv", base, head, "--")
				if patchErr == nil && len(patch) <= 64<<10 {
					masked, maskErr := secrets.Mask(patch)
					if maskErr == nil && strings.TrimSpace(masked.Text()) != "" {
						r.PR = &pr
						r.Code = "ready_for_review"
						r.ChangeEvidence = masked.Text()
					}
				}
			}
		}
		if r.PR == nil {
			outcome = "blocked_needs_input"
		}
	}
	// The artifact contains source-labeled observations, never an acceptance or
	// merge verdict. Strip remote prose before persisting; it is untrusted input.
	if r.PR != nil {
		for i := range r.PR.Reviews {
			r.PR.Reviews[i].Body = ""
		}
		r.PR.Comments = nil
	}
	evidence := mustJSON(r)
	masked, err := secrets.Mask(string(evidence))
	if err != nil {
		return
	}
	evidence = []byte(masked.Text())
	artifact := filepath.Join(taskRoot, "result.json")
	if r.Attempt > 1 {
		artifact = filepath.Join(taskRoot, fmt.Sprintf("result-%d.json", r.Attempt))
	}
	if err = os.WriteFile(artifact, evidence, 0600); err != nil {
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(evidence))
	now := time.Now()
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: r.ID, ExecutionGeneration: r.Attempt, Attempt: r.Attempt, Outcome: outcome, Producer: "child", TurnRef: work.TurnRef{ThreadID: thread.ID, TurnID: thread.TurnID, TurnStatus: status}, EmittedAt: now,
		Manifest: []work.Artifact{{Path: artifact, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "adapter-derived:" + r.ID, TaskID: r.ID, NodeID: s.cfg.Main, Path: artifact, SHA256: hash, ObservedAt: now}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	if err = service.Submit(ctx, run.ID, receipt); err != nil {
		r.Code = "receipt_failed"
		return
	}
	if err = s.finish(ctx, r, run.ID, status); err != nil {
		r.Code = "receipt_failed"
	} else {
		settled = true
	}
}
func taskPacket(r Record) (string, error) {
	definition, err := json.MarshalIndent(r.TaskDefinition(), "", "  ")
	if err != nil {
		return "", err
	}
	if r.Attempt > 1 {
		return skills.MethodPacket("Continue the same task from the current files, branch and receipts. Do not recreate or clean the worktree, repeat completed effects, or reset review and CI allowances.\n"+string(definition), r.Method)
	}
	return skills.MethodPacket("Implement this task definition in this worktree using your own setup. Create the named task branch from the detached base. Open the branch's pull request, wait for its checks and review, handle failures and report the result. Do not change protected base refs.\n"+string(definition), r.Method)
}
func hasHeading(body, anchor string) bool {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		title := strings.TrimSpace(strings.TrimLeft(line, "#"))
		slug := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
		if slug == anchor && anchor != "" {
			return true
		}
	}
	return false
}
func allowedFiles(files string, patterns []string) bool {
	if files == "" {
		return false
	}
	for _, file := range strings.Split(files, "\n") {
		allowed := false
		for _, pattern := range patterns {
			ok, _ := path.Match(pattern, file)
			allowed = allowed || ok
		}
		if !allowed {
			return false
		}
	}
	return true
}
