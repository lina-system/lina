// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/thisisjun786/lina/internal/core/worker/doing"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

// outcome carries both the dispatch's final save and its directory fence.
// A committed delivery may still have unknown effects. A quota wait may also
// retain an uncertain fence if closing a parked adapter failed.
type outcome uint8

const (
	outcomeFinished outcome = iota // No launch, or a settled quota attempt.
	outcomeUnknown
	outcomeQueued
	outcomeQueuedUnknown
	outcomeDelivered
	outcomeDeliveryUnknown // Delivery owns the save, even if its transaction fails.
)

func (o outcome) saveState() string {
	switch o {
	case outcomeFinished:
		return "finished"
	case outcomeUnknown:
		return "unknown"
	case outcomeQueued, outcomeQueuedUnknown:
		return "queued"
	case outcomeDelivered, outcomeDeliveryUnknown:
		return ""
	}
	panic("invalid dispatch outcome")
}

func (o outcome) queued() bool {
	return o == outcomeQueued || o == outcomeQueuedUnknown
}

func (o outcome) releaseReservations() bool {
	return o == outcomeFinished || o == outcomeQueued || o == outcomeDelivered
}

func (o outcome) waitForQuota() outcome {
	if o.releaseReservations() {
		return outcomeQueued
	}
	return outcomeQueuedUnknown
}

type execution struct {
	record       Record
	outcome      outcome
	observing    bool
	lease        lease.Lease
	git          func(string, ...string) (string, error)
	reservations []string
	deferred     []func()
	workspace    executionWorkspace
	attempt      executionAttempt
}

type executionWorkspace struct {
	repo, repository, workdir, taskRoot, base string
	protectedArgs                             []string
	protected, workProtected                  string
	protectedWorkRefs                         func() (string, error)
	directories                               []work.WorkerDirectory
}

type executionAttempt struct {
	thread            worker.Thread
	run               work.Run
	finish            worker.Finish
	observation       observation
	settledConnection bool
}

func (e *execution) closeDeferred() {
	// Native defers also finish the remaining cleanup if an adapter panics.
	for _, cleanup := range e.deferred {
		defer cleanup()
	}
	e.deferred = nil
}

func (s *Service) execute(ctx context.Context, record Record) {
	if s.cfg.ExecutionDeadline != nil {
		deadline, err := s.cfg.ExecutionDeadline(ctx, record)
		if err != nil {
			s.mu.Lock()
			delete(s.running, record.ID)
			s.mu.Unlock()
			return
		}
		if !deadline.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
	}
	e := execution{record: record}
	r := &e.record
	// Shutdown does not replay a possibly effective dispatch. Recovery marks the
	// running record unknown and retains the task's active reservation.
	observing := r.State == "unknown" && r.Thread != nil && r.WorkerName() == "codex"
	e.observing = observing
	if observing {
		e.outcome = outcomeUnknown
	}
	defer func() {
		if state := e.outcome.saveState(); ctx.Err() == nil && state != "" {
			// A terminal snapshot is recoverable only with its committed receipt.
			r.Snapshot = nil
			r.State = state
			_ = s.save(ctx, *r)
		}
		s.mu.Lock()
		delete(s.running, r.ID)
		s.mu.Unlock()
		if !e.outcome.queued() {
			s.Wake()
		}
	}()
	r.Code = "start_failed"
	if observing {
		r.Code = "reconciling"
	}
	if s.cfg.Work == nil || s.cfg.Area == nil {
		return
	}
	// Queued records from before shared allocation have no assigned branch.
	// Persist the assignment before creating any workspace or worker process.
	if r.Branch == "" {
		r.Branch = taskBranch(r.Definition.Branch, r.ID)
		if err := s.save(ctx, *r); err != nil {
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
	defer func() {
		// The adapter's later defer joins its process tree first. Join Git's
		// process owner too before releasing any directory reservation.
		if err := owner.Close(context.Background()); err != nil {
			e.outcome = outcomeUnknown
			r.Code = "cleanup_failed"
			return
		}
		if len(e.reservations) > 0 && e.outcome.releaseReservations() {
			if err := service.ReleaseWorker(context.Background(), e.reservations...); err != nil {
				e.outcome = outcomeUnknown
				r.Code = "reservation_unreleased"
			}
		}
	}()
	e.lease = l
	e.git = func(dir string, args ...string) (string, error) { return s.command(ctx, l, owner, dir, args...) }
	// Keep every attempt's deferred cleanup in execute's scope, including
	// the repeated close after delivery and the reverse order on handoff.
	defer e.closeDeferred()
	if !s.prepareWorkspace(ctx, &e) || !s.runAttempts(ctx, &e) {
		return
	}
	s.deliver(ctx, &e)
}

func (s *Service) prepareWorkspace(ctx context.Context, e *execution) bool {
	r, l, git := &e.record, e.lease, e.git
	service, observing := s.cfg.Work, e.observing
	d := r.TaskDefinition()
	repo, err := filepath.EvalSymlinks(d.Repository)
	if err != nil {
		return false
	}
	common, err := git(repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return false
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
	if !observing {
		if err = service.ReserveWorkerDirectories(ctx, directories); err != nil {
			r.Code = "admission_refused"
			if errors.Is(err, work.ErrControlHeld) {
				r.Code = "control_held"
			}
			return false
		}
	}
	for _, directory := range directories {
		e.reservations = append(e.reservations, directory.ID)
	}
	remote, err := git(repo, "remote", "get-url", "origin")
	if err != nil {
		return false
	}
	if !strings.HasPrefix(remote, "https://github.com/") && !strings.HasPrefix(remote, "git@github.com:") {
		r.Code = "repository_unsupported"
		return false
	}
	repository := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(remote, "https://github.com/"), "git@github.com:"), ".git")
	if strings.Count(repository, "/") != 1 || strings.ContainsAny(repository, ": \t\n") {
		r.Code = "repository_unsupported"
		return false
	}
	for _, ref := range []string{d.Base, d.Branch} {
		if _, err = git(repo, "check-ref-format", "--branch", ref); err != nil {
			r.Code = "invalid_branch"
			return false
		}
	}
	baseRef := r.BaseCommit
	if baseRef == "" {
		// Legacy queued records predate admission pinning.
		baseRef = d.Base
	}
	base, err := git(repo, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return false
	}
	r.BaseCommit = base
	protectedArgs := []string{"for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/remotes", "refs/lina"}
	protected, err := git(repo, protectedArgs...)
	if err != nil {
		return false
	}
	priorProtected := r.Protected
	r.Protected = map[string]string{}
	for _, line := range strings.Split(protected, "\n") {
		ref, head, ok := strings.Cut(line, " ")
		if ok && strings.HasPrefix(ref, "refs/heads/") {
			r.Protected[ref] = head
		}
	}
	if (observing || r.ReturnReceipt != "") && !maps.Equal(priorProtected, r.Protected) {
		r.Protected = priorProtected
		r.Code = "protected_ref_changed"
		return false
	}
	if r.ParentRefs == "" {
		if observing {
			return false
		}
		r.ParentRefs = protected
	} else {
		protected = r.ParentRefs
	}
	if err = s.save(ctx, *r); err != nil {
		return false
	}
	r.DesignText = map[string]string{}
	for _, ref := range d.DesignRefs {
		file, anchor, _ := strings.Cut(ref, "#")
		body, err := git(repo, "show", base+":"+file)
		if err != nil || !hasHeading(body, anchor) {
			r.Code = "design_reference_missing"
			return false
		}
		masked, maskErr := secrets.Mask(body)
		if maskErr != nil {
			return false
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
		return false
	}
	if !observing {
		if _, err = s.cfg.Area.Prepare(ctx, l, r.ID, workspace.Spec); err != nil {
			return false
		}
		if _, err = git(workdir, "remote", "set-url", "origin", remote); err != nil {
			return false
		}
		if err = os.MkdirAll(taskRoot, 0700); err != nil {
			return false
		}
	} else if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		return false
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
		return false
	}
	if r.WorkRefs == "" {
		if observing {
			return false
		}
		r.WorkRefs = workProtected
	} else {
		workProtected = r.WorkRefs
	}
	if err = s.save(ctx, *r); err != nil {
		return false
	}
	if r.Packet == "" {
		r.Packet, err = taskPacket(*r)
		if err != nil || s.save(ctx, *r) != nil {
			return false
		}
	}
	e.workspace = executionWorkspace{repo: repo, repository: repository, workdir: workdir, taskRoot: taskRoot, base: base,
		protectedArgs: protectedArgs, protected: protected, workProtected: workProtected, protectedWorkRefs: protectedWorkRefs, directories: directories}
	return true
}

func (s *Service) runAttempts(ctx context.Context, e *execution) bool {
	r, l := &e.record, e.lease
	service, observing := s.cfg.Work, e.observing
	workdir, directories := e.workspace.workdir, e.workspace.directories
	var err error
	var thread worker.Thread
	var run work.Run
	var finish worker.Finish
	var observation observation
	var settledConnection bool
	if observing {
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		snap, readErr := s.observeThread(readCtx, l, r, workdir)
		cancel()
		if readErr != nil || snap.Finish == nil {
			return false
		}
		thread = snap.Thread
		finish = *snap.Finish
		r.Snapshot = &worker.ThreadSnapshot{Thread: thread, State: snap.State, Finish: snap.Finish, EffectsKnown: snap.EffectsKnown}
		run = work.Run{ID: thread.AttemptID, TaskID: r.ID, Generation: r.Attempt, Worker: r.WorkerName(), ThreadID: thread.ID, TurnID: thread.TurnID, NodeID: s.cfg.Main}
		card, readErr := service.Get(ctx, r.ID)
		if readErr != nil {
			return false
		}
		if card.Run == nil || card.Run.ID != run.ID {
			if err = service.Start(ctx, run); err != nil {
				return false
			}
		}
		observation = func() (json.RawMessage, []antigravity.DeniedAction) {
			return mustJSON(map[string]any{"output": snap.Output, "finish": snap.Finish}), nil
		}
	} else {
		for len(r.Tried) < 2 {
			for _, directory := range directories {
				if err = service.CheckWorker(ctx, directory.ID); err != nil {
					r.Code = "control_held"
					return false
				}
			}
			adapter, closeAdapter, selectErr := s.selectWorker(ctx, l, *r)
			if selectErr != nil {
				return false
			}
			if adapter == nil {
				r.Wait = "worker_quota"
				e.outcome = e.outcome.waitForQuota()
				return false
			}
			closeWorker := closeAdapter
			closeAdapter = func(closeCtx context.Context) error {
				closeErr := closeWorker(closeCtx)
				if closeErr != nil {
					e.outcome = outcomeUnknown
					r.Code = "cleanup_failed"
				}
				return closeErr
			}
			e.deferred = append(e.deferred, func() { closeAdapter(context.Background()) })
			observation = observeAdapter(adapter)
			r.Version, err = adapter.Version(ctx)
			if err != nil {
				closeAdapter(context.Background())
				return false
			}
			if err = s.reserveWorker(ctx, r, adapter.Name()); err != nil {
				closeAdapter(context.Background())
				if errors.Is(err, errWorkerParked) {
					continue
				}
				return false
			}
			run, err = s.nextRun(ctx, *r)
			if err != nil {
				return false
			}
			// Readiness refusals consume chooser slots, but create no work run.
			// Recovery and completion must retain the actual run's generation.
			r.Attempt = run.Generation
			if err = s.save(ctx, *r); err != nil {
				return false
			}
			if r.Recoveries > 0 && s.cfg.AuthorizeRecovery != nil {
				allowed, err := s.cfg.AuthorizeRecovery(ctx, *r)
				if err != nil || !allowed {
					r.Code = "recovery_scope_changed"
					closeAdapter(context.Background())
					return false
				}
			}
			// reserveWorker persisted this attempt before any launch. A crash is
			// uncertain, never permission to consume another replacement.
			for _, directory := range directories {
				if err = service.CheckWorker(ctx, directory.ID); err != nil {
					r.Code = "control_held"
					return false
				}
			}
			e.outcome = outcomeUnknown
			thread, err = s.startAttempt(ctx, l, adapter, *r, run.ID, workdir)
			if err != nil {
				if errors.Is(err, worker.ErrNeedsSignIn) {
					if closeAdapter(ctx) != nil {
						return false
					}
					e.outcome = outcomeFinished
					if err = s.needsSignIn(ctx, r); err != nil {
						return false
					}
					continue
				}
				closeAdapter(context.Background())
				return false
			}
			// ACP agentInfo is available only after the session handshake.
			r.Version, err = adapter.Version(ctx)
			if err == nil {
				err = s.save(ctx, *r)
			}
			if err != nil {
				closeAdapter(context.Background())
				return false
			}
			var cancelled bool
			_ = s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(record,'$.canceled'),0) FROM worker_dispatch WHERE id=?`, r.ID).Scan(&cancelled)
			})
			if cancelled {
				r.Canceled = true
				_ = adapter.Interrupt(ctx, thread, worker.UserCancel)
			}
			run.ThreadID, run.TurnID = thread.ID, thread.TurnID
			r.Thread = &thread
			if err = s.save(ctx, *r); err != nil {
				closeAdapter(context.Background())
				return false
			}
			e.outcome = outcomeUnknown
			if err = service.Start(ctx, run); err != nil {
				closeAdapter(context.Background())
				return false
			}
			handle, unbind := func(worker.Event) {}, func() {}
			if s.cfg.Bind != nil {
				handle, unbind = s.cfg.Bind(*r, adapter, thread, r.Packet, workdir)
			}
			observed := false
			var progressSequence uint64
			if err = s.reportReadiness(ctx, *r, run, &progressSequence); err != nil {
				unbind()
				return false
			}
			var progress progressBuffer
			var activity string
			var activitySequence uint64
			var lastDoing doing.Snapshot
			var doingSequence uint64
			progressTick := time.NewTicker(progressInterval)
			e.deferred = append(e.deferred, progressTick.Stop)
			recordProgress := func(summary string, progressErr error) error {
				if progressErr != nil || summary == "" {
					return progressErr
				}
				progressSequence++
				return service.ReportProgress(ctx, r.ID, run.ID, progressSequence, summary)
			}
			for !observed {
				select {
				case event, ok := <-adapter.Events():
					if !ok || event.Thread != thread || event.Validate() != nil {
						r.Code = "worker_protocol_error"
						unbind()
						closeAdapter(context.Background())
						return false
					}
					if event.Progress != nil {
						err = recordProgress(progress.Observe(*event.Progress, time.Now()))
					} else if event.Finish != nil {
						err = recordProgress(progress.Flush(time.Now(), true))
					}
					if event.Doing != nil && err == nil && *event.Doing != lastDoing {
						doingSequence++
						err = service.ReportDoing(ctx, r.ID, run.ID, doingSequence, *event.Doing)
						if err == nil {
							lastDoing = *event.Doing
						}
					}
					if next := worker.Activity(event); err == nil && next != "" && next != activity {
						activitySequence++
						err = service.ReportActivity(ctx, r.ID, run.ID, activitySequence, next)
						if err == nil {
							activity = next
						}
					}
					if err != nil {
						r.Code = "progress_failed"
						unbind()
						closeAdapter(context.Background())
						return false
					}
					if event.Approval != nil || event.Question != nil || event.ToolCall != nil || event.Progress != nil && event.Progress.Phase == "server_request_resolved" {
						if r.PendingRequests == nil {
							r.PendingRequests = map[string]bool{}
						}
						switch {
						case event.Approval != nil:
							r.PendingRequests[event.Approval.ID] = true
						case event.Question != nil:
							r.PendingRequests[event.Question.ID] = true
						case event.ToolCall != nil:
							r.PendingRequests[event.ToolCall.ID] = true
						default:
							delete(r.PendingRequests, event.Progress.ItemID)
						}
						if err = s.save(ctx, *r); err != nil {
							unbind()
							closeAdapter(context.Background())
							return false
						}
					}
					handle(event)
					if event.Finish != nil {
						finish = *event.Finish
						observed = true
					}
				case now := <-progressTick.C:
					if err = recordProgress(progress.Flush(now, false)); err != nil {
						r.Code = "progress_failed"
						unbind()
						closeAdapter(context.Background())
						return false
					}
				case <-ctx.Done():
					unbind()
					closeAdapter(context.Background())
					return false
				}
			}
			progressTick.Stop()
			quotaStop := finish.Status == worker.Failed && (finish.Failure == worker.QuotaFailure || finish.Failure == worker.RateLimitFailure || finish.Failure == worker.LoginFailure)
			if quotaStop {
				err = s.park(ctx, adapter)
			}
			snapshot, snapshotErr := terminalSnapshot(ctx, adapter, thread)
			if snapshotErr == nil {
				r.Snapshot = &worker.ThreadSnapshot{Thread: snapshot.Thread, State: snapshot.State, Finish: snapshot.Finish, EffectsKnown: snapshot.EffectsKnown && len(snapshot.Pending) == 0 && len(r.PendingRequests) == 0}
			}
			if r.Snapshot == nil || r.Snapshot.Finish == nil {
				r.Snapshot = &worker.ThreadSnapshot{Thread: thread, State: worker.Unknown, Finish: &finish}
			}
			_, canRecoverAttempt := adapter.(attemptRecoverer)
			settledConnection = canRecoverAttempt && r.Snapshot.EffectsKnown
			unbind()
			// No second worker may touch this worktree until the first process
			// and all of its descendants have been reaped.
			closeErr := closeAdapter(ctx)
			if err != nil || closeErr != nil {
				return false
			}
			if quotaStop && len(r.Tried) == 1 && r.Snapshot.EffectsKnown {
				r.Snapshot = nil
				r.PendingRequests = nil
				if err = service.Finish(ctx, r.ID, run.ID, "failed"); err != nil {
					return false
				}
				e.outcome = outcomeFinished
				continue
			}
			break
		}
	}
	if thread.ID == "" {
		// Refused sessions create no worker turn or receipt.
		return false
	}
	e.attempt = executionAttempt{thread: thread, run: run, finish: finish, observation: observation, settledConnection: settledConnection}
	return true
}

func (s *Service) deliver(ctx context.Context, e *execution) {
	r, git := &e.record, e.git
	d := r.TaskDefinition()
	w := e.workspace
	repo, repository, workdir, taskRoot, base := w.repo, w.repository, w.workdir, w.taskRoot, w.base
	protectedArgs, protected, workProtected, protectedWorkRefs := w.protectedArgs, w.protected, w.workProtected, w.protectedWorkRefs
	thread, run, finish, observation := e.attempt.thread, e.attempt.run, e.attempt.finish, e.attempt.observation
	// Claude and Antigravity synthesize connection failures without a result.
	// Codex alone also reports observed provider failures as "connection";
	// its transport-loss finish is synthetic and must remain unknown too.
	// ACP may instead supply a joined process with a complete, effect-free
	// terminal snapshot. That evidence admits the shared recovery policy.
	if finish.Failure == worker.ConnectionFailure && !e.attempt.settledConnection && !(r.WorkerName() == "codex" && finish.Code == "connection") {
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
	if err = json.Unmarshal([]byte(observed.Text()), r); err != nil {
		r.Code = "invalid_result"
		return
	}
	status, receiptOutcome := "completed", "ready_for_review"
	if finish.Status != worker.Done {
		status, receiptOutcome = "failed", "failed"
		if finish.Status == worker.Interrupted {
			status, receiptOutcome = "interrupted", "interrupted"
		}
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
			receiptOutcome = "blocked_needs_input"
		}
	}
	// The artifact contains source-labeled observations, never an acceptance or
	// merge verdict. Strip remote prose before persisting; it is untrusted input.
	if r.PR != nil {
		r.FailureReason = r.PR.FailureReason()
		if masked, err := secrets.Mask(r.FailureReason); err == nil {
			r.FailureReason = masked.Text()
		} else {
			r.FailureReason = ""
		}
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
	if r.Attempt > 1 || r.GenerationBase > 0 {
		artifact = filepath.Join(taskRoot, fmt.Sprintf("result-%d.json", run.Generation))
	}
	if err = os.WriteFile(artifact, evidence, 0600); err != nil {
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(evidence))
	now := time.Now()
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: r.ID, ExecutionGeneration: run.Generation, Attempt: run.Generation, Outcome: receiptOutcome, Producer: "child", TurnRef: work.TurnRef{ThreadID: thread.ID, TurnID: thread.TurnID, TurnStatus: status}, EmittedAt: now,
		Manifest: []work.Artifact{{Path: artifact, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "adapter-derived:" + r.ID, TaskID: r.ID, NodeID: s.cfg.Main, Path: artifact, SHA256: hash, ObservedAt: now}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	e.outcome = outcomeDeliveryUnknown
	if err = s.finish(ctx, *r, run.ID, status, receipt); err != nil {
		r.Code = "receipt_failed"
	} else {
		// A committed terminal receipt can still describe unknown effects.
		// Only the committed recovery state can release its directory fence.
		var state string
		readErr := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT state FROM worker_dispatch WHERE id=?", r.ID).Scan(&state)
		})
		if readErr == nil && state != "unknown" {
			e.outcome = outcomeDelivered
		}
	}
}
func taskPacket(r Record) (string, error) {
	definition, err := json.MarshalIndent(r.TaskDefinition(), "", "  ")
	if err != nil {
		return "", err
	}
	if r.Recoveries > 0 {
		return skills.MethodPacket(fmt.Sprintf("Assignment: %s\nBase commit: %s\n", r.ID, r.BaseCommit)+"Continue the same task from the current files, branch and receipts. Do not recreate or clean the worktree, repeat completed effects, or reset review and CI allowances.\n"+string(definition), r.Method)
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
