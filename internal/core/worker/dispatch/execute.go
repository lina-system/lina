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

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func (s *Service) execute(r Record) {
	// Shutdown does not replay a possibly effective dispatch. Recovery marks the
	// running record unknown and retains the task's active reservation.
	ctx := s.ctx
	admitted, settled := false, false
	defer func() {
		if ctx.Err() == nil {
			r.State = "finished"
			if admitted && !settled {
				r.State = "unknown"
			}
			_ = s.save(ctx, r)
		}
	}()
	r.Code = "start_failed"
	l, err := s.cfg.Activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "delegated worker task"})
	if err != nil {
		return
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	git := func(dir string, args ...string) (string, error) { return s.command(ctx, l, owner, dir, args...) }
	d := r.Definition
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
	protectedArgs := []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/main", "refs/heads/dev", "refs/remotes/origin/main", "refs/remotes/origin/dev"}
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
	taskRoot := filepath.Join(s.cfg.Root, "work", "tasks", r.ID)
	workdir := filepath.Join(s.cfg.Root, "work", "worktrees", r.ID)
	if err = os.MkdirAll(taskRoot, 0700); err != nil {
		return
	}
	if err = os.MkdirAll(filepath.Dir(workdir), 0700); err != nil {
		return
	}
	service := work.New(s.cfg.DB)
	if err = service.Create(ctx, work.Task{ID: r.ID, PersonID: r.Origin.Person, AgentID: r.Origin.Agent, ConversationID: r.Origin.Conversation, Title: d.What, Done: d.Done, ArtifactRoots: []string{taskRoot}, RequiresApply: true}); err != nil {
		return
	}
	if _, err = git(repo, "worktree", "add", "-b", d.Branch, workdir, base); err != nil {
		return
	}
	adapter := antigravity.New(antigravity.Config{Binary: s.cfg.Binary, Environment: s.cfg.Environment, Epoch: s.cfg.Epoch, Leaser: s.cfg.Activities})
	defer adapter.Close(context.Background())
	r.Version, err = adapter.Version(ctx)
	if err != nil {
		return
	}
	packet, err := taskPacket(r)
	if err != nil {
		return
	}
	thread, err := adapter.Start(ctx, l, worker.StartSpec{TaskID: r.ID, AttemptID: r.ID + ":1", Workdir: workdir, TaskPacket: packet})
	if err != nil {
		return
	}
	run := work.Run{ID: r.ID + ":1", TaskID: r.ID, Generation: 1, Worker: "antigravity", ThreadID: thread.ID, TurnID: thread.TurnID, NodeID: s.cfg.Main}
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
	r.State = "finished"
	result := adapter.Result()
	r.Denied = result.DeniedActions
	status, outcome := "completed", "ready_for_review"
	if finish.Status != worker.Done {
		status, outcome = "failed", "failed"
		r.Code = finish.Code
	} else {
		r.Code = "pr_unverified"
		after, e := git(repo, protectedArgs...)
		head, headErr := git(workdir, "rev-parse", "HEAD")
		branch, branchErr := git(workdir, "branch", "--show-current")
		dirty, dirtyErr := git(workdir, "status", "--porcelain")
		files, filesErr := git(workdir, "diff", "--name-only", base+".."+head)
		if e == nil && after == protected && headErr == nil && head != base && branchErr == nil && branch == d.Branch && dirtyErr == nil && dirty == "" && filesErr == nil && allowedFiles(files, d.WriteArea) {
			pr, prErr := antigravity.ReadPullRequest(ctx, s.cfg.PR, repository, d.Branch, d.Base, head)
			if prErr == nil {
				r.PR = &pr
				r.Code = "ready_for_review"
			}
		}
		if r.PR == nil {
			outcome = "blocked_needs_input"
		}
	}
	// The artifact contains source-labeled observations, never an acceptance or
	// merge verdict. Strip remote prose before persisting; it is untrusted input.
	if r.PR != nil {
		r.PR.Reviews = nil
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
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: r.ID, ExecutionGeneration: 1, Attempt: 1, Outcome: outcome, Producer: "child", TurnRef: work.TurnRef{ThreadID: thread.ID, TurnID: thread.TurnID, TurnStatus: status}, EmittedAt: now,
		Manifest: []work.Artifact{{Path: artifact, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "adapter-derived:" + r.ID, TaskID: r.ID, NodeID: s.cfg.Main, Path: artifact, SHA256: hash, ObservedAt: now}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	if err = service.Submit(ctx, run.ID, receipt); err != nil {
		r.Code = "receipt_failed"
		return
	}
	if err = service.Finish(ctx, r.ID, run.ID, status); err != nil {
		r.Code = "receipt_failed"
	} else {
		settled = true
	}
}
func taskPacket(r Record) (string, error) {
	definition, err := json.MarshalIndent(r.Definition, "", "  ")
	if err != nil {
		return "", err
	}
	return skills.MethodPacket("Implement this task definition in this worktree using your own setup. Open the branch's pull request, wait for its checks and review, handle failures and report the result. Do not change protected base refs.\n"+string(definition), r.Method)
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
