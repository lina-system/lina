// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

func (s *Service) execute(ctx context.Context, r Record) {
	// Shutdown does not replay a possibly effective dispatch. Recovery marks the
	// running record unknown and retains the task's active reservation.
	admitted, settled := false, false
	deliverySaved := false
	queued := false
	defer func() {
		if ctx.Err() == nil && !deliverySaved {
			r.State = "finished"
			if queued {
				r.State = "queued"
			} else if admitted && !settled {
				r.State = "unknown"
			}
			_ = s.save(ctx, r)
		}
		s.mu.Lock()
		delete(s.running, r.ID)
		s.mu.Unlock()
		if !queued {
			s.Wake()
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
	service := s.cfg.Work
	var reservations []string
	defer func() {
		// The adapter's later defer joins its process tree first. Join Git's
		// process owner too before releasing any directory reservation.
		if err := owner.Close(context.Background()); err != nil {
			admitted, settled = true, false
			queued, deliverySaved = false, false
			r.Code = "cleanup_failed"
			return
		}
		if len(reservations) > 0 && (!admitted || settled) {
			if err := service.ReleaseWorker(context.Background(), reservations...); err != nil {
				admitted, settled = true, false
				queued, deliverySaved = false, false
				r.Code = "reservation_unreleased"
			}
		}
	}()
	git := func(dir string, args ...string) (string, error) { return s.command(ctx, l, owner, dir, args...) }
	d := r.TaskDefinition()
	repo, err := filepath.EvalSymlinks(d.Repository)
	if err != nil {
		return
	}
	common, err := git(repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	workdir := filepath.Join(s.cfg.Root, "work", "tasks", r.ID)
	// Keep adapter evidence outside the worker checkout. Both destinations are
	// reserved before shared allocation creates any workspace or artifacts.
	taskRoot := filepath.Join(s.cfg.Root, "work", "runs", r.ID)
	repositoryID := fmt.Sprintf("%x", sha256.Sum256([]byte(repo)))
	directories := []work.WorkerDirectory{
		{ID: r.ID + ":repository", Path: repo},
		{ID: r.ID + ":git", Path: common},
		{ID: r.ID + ":clone", Path: filepath.Join(s.cfg.Root, "work", "clones", repositoryID)},
		{ID: r.ID + ":worktree", Path: workdir},
		{ID: r.ID + ":artifacts", Path: taskRoot},
	}
	if err = service.ReserveWorkerDirectories(ctx, directories); err != nil {
		r.Code = "admission_refused"
		if errors.Is(err, work.ErrControlHeld) {
			r.Code = "control_held"
		}
		return
	}
	for _, directory := range directories {
		reservations = append(reservations, directory.ID)
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
	baseRef := r.BaseCommit
	if baseRef == "" {
		// Legacy queued records predate admission pinning.
		baseRef = d.Base
	}
	base, err := git(repo, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return
	}
	r.BaseCommit = base
	protectedArgs := []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes", "refs/lina"}
	protected, err := git(repo, protectedArgs...)
	if err != nil {
		return
	}
	r.Protected = map[string]string{}
	for _, line := range strings.Split(protected, "\n") {
		ref, head, ok := strings.Cut(line, " ")
		if ok && strings.HasPrefix(ref, "refs/heads/") {
			r.Protected[ref] = head
		}
	}
	if err = s.save(ctx, r); err != nil {
		return
	}
	r.DesignText = map[string]string{}
	for _, ref := range d.DesignRefs {
		file, anchor, _ := strings.Cut(ref, "#")
		body, err := git(repo, "show", base+":"+file)
		if err != nil || !hasHeading(body, anchor) {
			r.Code = "design_reference_missing"
			return
		}
		masked, maskErr := secrets.Mask(body)
		if maskErr != nil {
			return
		}
		r.DesignText[ref] = masked.DerivedText()
	}
	workspace := work.Workspace{TaskID: r.ID, Path: workdir, Spec: work.WorkspaceSpec{
		RepositoryID: repositoryID, Source: repo, Base: base, Branch: d.Branch,
	}}
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
			if protectedTaskRef(ref, r.ID) {
				protected = append(protected, ref)
			}
		}
		return strings.Join(protected, "\n"), err
	}
	workProtected, err := protectedWorkRefs()
	if err != nil {
		return
	}
	if r.Packet == "" {
		r.Packet, err = taskPacket(r)
		if err != nil || s.save(ctx, r) != nil {
			return
		}
	}
	var thread worker.Thread
	var run work.Run
	var finish worker.Finish
	var observation observation
	for len(r.Tried) < 2 {
		for _, directory := range directories {
			if err = service.CheckWorker(ctx, directory.ID); err != nil {
				r.Code = "control_held"
				return
			}
		}
		adapter, closeAdapter, selectErr := s.selectWorker(ctx, l, r)
		if selectErr != nil {
			return
		}
		if adapter == nil {
			r.Wait = "worker_quota"
			queued = true
			return
		}
		closeWorker := closeAdapter
		closeAdapter = func(closeCtx context.Context) error {
			closeErr := closeWorker(closeCtx)
			if closeErr != nil {
				admitted, settled = true, false
				queued, deliverySaved = false, false
				r.Code = "cleanup_failed"
			}
			return closeErr
		}
		defer closeAdapter(context.Background())
		observation = observeAdapter(adapter)
		r.Version, err = adapter.Version(ctx)
		if err != nil {
			closeAdapter(context.Background())
			return
		}
		if err = s.reserveWorker(ctx, &r, adapter.Name()); err != nil {
			closeAdapter(context.Background())
			if errors.Is(err, errWorkerParked) {
				continue
			}
			return
		}
		generation := len(r.Tried)
		run = work.Run{ID: fmt.Sprintf("%s:%d", r.ID, generation), TaskID: r.ID, Generation: generation, Worker: r.Worker, NodeID: s.cfg.Main}
		// reserveWorker persisted this attempt before any launch. A crash is
		// uncertain, never permission to consume another replacement.
		for _, directory := range directories {
			if err = service.CheckWorker(ctx, directory.ID); err != nil {
				r.Code = "control_held"
				return
			}
		}
		// A replacement launch can have effects before returning an error too.
		admitted, settled = true, false
		thread, err = adapter.Start(ctx, l, worker.StartSpec{TaskID: r.ID, AttemptID: run.ID, Workdir: workdir, TaskPacket: r.Packet})
		if err != nil {
			admitted = true
			closeAdapter(context.Background())
			return
		}
		run.ThreadID, run.TurnID = thread.ID, thread.TurnID
		admitted, settled = true, false
		if err = service.Start(ctx, run); err != nil {
			closeAdapter(context.Background())
			return
		}
		handle, unbind := func(worker.Event) {}, func() {}
		if s.cfg.Bind != nil {
			handle, unbind = s.cfg.Bind(r, adapter, thread, r.Packet, workdir)
		}
		observed := false
		for !observed {
			select {
			case event, ok := <-adapter.Events():
				if !ok || event.Thread != thread || event.Validate() != nil {
					r.Code = "worker_protocol_error"
					unbind()
					closeAdapter(context.Background())
					return
				}
				handle(event)
				if event.Finish != nil {
					finish = *event.Finish
					observed = true
				}
			case <-ctx.Done():
				unbind()
				closeAdapter(context.Background())
				return
			}
		}
		quotaStop := finish.Status == worker.Failed && (finish.Failure == worker.QuotaFailure || finish.Failure == worker.RateLimitFailure || finish.Failure == worker.LoginFailure)
		if quotaStop {
			err = s.park(ctx, adapter)
		}
		unbind()
		// No second worker may touch this worktree until the first process
		// and all of its descendants have been reaped.
		closeErr := closeAdapter(ctx)
		if err != nil || closeErr != nil {
			return
		}
		if quotaStop && len(r.Tried) == 1 {
			if err = service.Finish(ctx, r.ID, run.ID, "failed"); err != nil {
				return
			}
			settled = true
			continue
		}
		break
	}
	if finish.Failure == worker.ConnectionFailure && finish.Code == "missing_result" {
		r.Code = finish.Code
		return
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
		files, filesErr := git(workdir, "diff", "--name-only", "--no-renames", "-z", base+".."+head, "--")
		if e == nil && after == protected && workErr == nil && workAfter == workProtected && headErr == nil && head != base && branchErr == nil && branch == d.Branch && dirtyErr == nil && dirty == "" && filesErr == nil {
			r.ChangedFiles = strings.Split(strings.TrimSuffix(files, "\x00"), "\x00")
			pr, prErr := antigravity.ReadPullRequest(ctx, s.cfg.PR, repository, d.Branch, d.Base, head)
			identityOK := pr.Number > 0 && pr.URL == fmt.Sprintf("https://github.com/%s/pull/%d", repository, pr.Number) && pr.Branch == d.Branch && pr.Base == d.Base && pr.Head == head
			if (prErr == nil || errors.Is(prErr, antigravity.ErrPullRequestNotReady)) && identityOK {
				r.PR = &pr
				r.Code = "change_evidence_unavailable"
				patch, patchErr := git(workdir, "diff", "--no-ext-diff", "--no-textconv", base, head, "--")
				if patchErr == nil && len(patch) <= 64<<10 {
					masked, maskErr := secrets.Mask(patch)
					if maskErr == nil && strings.TrimSpace(masked.Text()) != "" {
						r.PR = &pr
						r.Code = "ready_for_review"
						r.ChangeEvidence = masked.Text()
						if prErr != nil {
							r.Code = "pr_unverified"
						}
					}
				}
			}
		}
		if r.PR == nil || r.Code != "ready_for_review" {
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
	if err = os.WriteFile(artifact, evidence, 0600); err != nil {
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(evidence))
	now := time.Now()
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: r.ID, ExecutionGeneration: run.Generation, Attempt: run.Generation, Outcome: outcome, Producer: "child", TurnRef: work.TurnRef{ThreadID: thread.ID, TurnID: thread.TurnID, TurnStatus: status}, EmittedAt: now,
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
		settled, deliverySaved = true, true
	}
}
func taskPacket(r Record) (string, error) {
	definition, err := json.MarshalIndent(r.TaskDefinition(), "", "  ")
	if err != nil {
		return "", err
	}
	return skills.MethodPacket(fmt.Sprintf("Assignment: %s\nBase commit: %s\nCompletion: report the final worker result and PR; Core derives an adapter receipt from observed evidence. No additional material or memory projections are granted.\n", r.ID, r.BaseCommit)+"Implement this task definition in this worktree using your own setup. Create the named task branch from the detached base, or continue it if an earlier attempt already created it. Preserve partial work from an earlier attempt. Open the branch's pull request, wait for its checks and review, handle failures and report the result. Do not change protected base refs.\n"+string(definition), r.Method)
}
func allowedFiles(files string, patterns []string) bool {
	if files == "" {
		return false
	}
	for _, file := range strings.Split(files, "\n") {
		allowed := false
		for _, pattern := range patterns {
			ok := work.AreaContains(pattern, file)
			allowed = allowed || ok
		}
		if !allowed {
			return false
		}
	}
	return true
}
