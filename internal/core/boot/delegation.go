// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

// Delegation is opt-in installation configuration, never a model-selected
// executable, environment, repository path or capacity assertion.
type delegationConfig struct {
	Codex        string                          `json:"codex"`
	Git          string                          `json:"git"`
	GH           string                          `json:"gh"`
	Slots        int                             `json:"slots"`
	Repositories map[string]delegationRepository `json:"repositories"`
}
type delegationRepository struct {
	Clone string `json:"clone"`
	Base  string `json:"base"`
}
type delegationRuntime struct {
	root          *conversationRuntime
	config        delegationConfig
	reactor       *reactor.Reactor
	adapter       *codex.Session
	processes     *proc.Owner
	cancel        context.CancelFunc
	done          chan struct{}
	mu            sync.Mutex
	threads       map[string]worker.Thread
	leases        map[string]lease.Lease
	connection    lease.Lease
	reset         time.Time
	resetChanged  chan struct{}
	quotaChanged  chan struct{}
	adapters      map[string]*codex.Session
	processLeases []lease.Lease
	workerEvents  chan worker.Event
	wg            sync.WaitGroup
	ctx           context.Context
}

var gitObject = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (r *conversationRuntime) startDelegation(ctx context.Context) error {
	file, err := os.Open(filepath.Join(r.root, "system", "core", "delegation.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	var cfg delegationConfig
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.Codex) || !filepath.IsAbs(cfg.Git) || !filepath.IsAbs(cfg.GH) || cfg.Slots < 0 {
		return errors.New("invalid delegation installation")
	}
	for name, repo := range cfg.Repositories {
		if !repositoryName.MatchString(name) || name != strings.ToLower(name) || !filepath.IsAbs(repo.Clone) || repo.Base == "" || strings.HasPrefix(repo.Base, "-") {
			return errors.New("invalid delegated repository")
		}
	}
	runctx, cancel := context.WithCancel(ctx)
	d := &delegationRuntime{root: r, config: cfg, processes: proc.NewOwner(), cancel: cancel, done: make(chan struct{}), threads: map[string]worker.Thread{}, leases: map[string]lease.Lease{}, resetChanged: make(chan struct{}, 1), quotaChanged: make(chan struct{}, 1), adapters: map[string]*codex.Session{}, workerEvents: make(chan worker.Event, 256), ctx: runctx}
	d.connection, err = r.activities.Acquire(runctx, lease.Activity{Class: lease.Reported, Reason: "delegated worker connection"})
	if err != nil {
		cancel()
		return err
	}
	d.adapter, err = codex.Open(runctx, d.connection, codex.Config{Executable: cfg.Codex})
	if err != nil {
		cancel()
		d.connection.Release()
		return err
	}
	d.reactor = reactor.New(r.work, reactor.Hooks{Availability: d.availability, Start: d.start, ReadPR: d.readPR, Return: d.returnConflict, Changed: func(ctx context.Context) { _ = r.publishRunning(ctx) }})
	r.delegation = d
	go d.events(runctx)
	// Recovery never repeats an armed dispatch. Queued tasks can run immediately;
	// old active turns and uncertain sends retain their leases for reconciliation.
	return d.reactor.Wake(ctx)
}
func (d *delegationRuntime) close(ctx context.Context) error {
	d.cancel()
	err := d.adapter.Close()
	d.mu.Lock()
	adapters := make([]*codex.Session, 0, len(d.adapters))
	for _, a := range d.adapters {
		adapters = append(adapters, a)
	}
	d.mu.Unlock()
	for _, a := range adapters {
		err = errors.Join(err, a.Close())
	}
	d.wg.Wait()
	err = errors.Join(err, d.processes.Close(ctx))
	select {
	case <-d.done:
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
	d.mu.Lock()
	for _, l := range d.processLeases {
		l.Release()
	}
	for _, l := range d.leases {
		l.Release()
	}
	d.mu.Unlock()
	d.connection.Release()
	return err
}
func (d *delegationRuntime) availability(ctx context.Context) (work.Availability, error) {
	raw, err := os.ReadFile(filepath.Join(d.root.root, "system", "core", "delegation.json"))
	if err != nil {
		return work.Availability{}, err
	}
	var report delegationConfig
	if err = json.Unmarshal(raw, &report); err != nil || report.Slots < 0 {
		return work.Availability{}, errors.New("invalid host capacity report")
	}
	rows, err := d.adapter.Quota(ctx)
	d.mu.Lock()
	sessions := make([]*codex.Session, 0, len(d.adapters))
	for _, s := range d.adapters {
		sessions = append(sessions, s)
	}
	d.mu.Unlock()
	freshest := func(rows []provider.QuotaRow) time.Time {
		var latest time.Time
		for _, row := range rows {
			if row.Observed.After(latest) {
				latest = row.Observed
			}
		}
		return latest
	}
	latest := freshest(rows)
	for _, session := range sessions {
		observed, e := session.Quota(ctx)
		if e != nil {
			continue
		}
		if at := freshest(observed); at.After(latest) {
			rows = observed
			latest = at
			err = nil
		}
	}
	if err != nil {
		return work.Availability{}, err
	}
	blocked := false
	var reset time.Time
	for _, row := range rows {
		if row.UsedPct != nil && *row.UsedPct >= 100 && (row.ResetsAt == nil || row.ResetsAt.After(time.Now())) {
			blocked = true
			if row.ResetsAt != nil && (reset.IsZero() || row.ResetsAt.Before(reset)) {
				reset = *row.ResetsAt
			}
		}
	}
	d.mu.Lock()
	changed := !d.reset.Equal(reset)
	d.reset = reset
	d.mu.Unlock()
	if changed {
		select {
		case d.resetChanged <- struct{}{}:
		default:
		}
	}
	return work.Availability{Hosts: map[string]int{"local": report.Slots}, BlockedWorkers: map[string]bool{"codex": blocked}}, nil
}

// command uses fixed argument vectors and bounds command output. GitHub reads
// remain observation only; this scheduler never merges on a worker's claim.
func (d *delegationRuntime) command(ctx context.Context, path, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := d.root.activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "delegated work repository observation"})
	if err != nil {
		return nil, err
	}
	defer l.Release()
	var out limitedOutput
	err = d.processes.Run(ctx, l, proc.Command{Path: path, Args: args, Dir: dir, Env: proc.InheritedEnvironment("LINA_RESPONSES_API_KEY"), Stdout: &out, Stderr: io.Discard})
	return out.Bytes(), err
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("command output too large")
	}
	return b.Buffer.Write(p)
}
func (d *delegationRuntime) start(ctx context.Context, c work.Card) (run work.Run, err error) {
	sent := false
	defer func() {
		if err != nil && !sent {
			err = errors.Join(reactor.ErrNotStarted, err)
			d.mu.Lock()
			activity := d.leases["pending:"+c.Task.ID]
			delete(d.leases, "pending:"+c.Task.ID)
			d.mu.Unlock()
			activity.Release()
		}
	}()
	p := c.Task.Schedule
	repo, ok := d.config.Repositories[p.Repository]
	if !ok || p.Worker != "codex" || p.Host != "local" {
		return work.Run{}, work.ErrInvalid
	}
	origin, err := d.root.router.view(ctx, clientsrv.Session{PersonID: c.Task.PersonID}, c.Task.AgentID)
	if err != nil {
		return work.Run{}, err
	}
	input, err := origin.journal().Get(ctx, p.InputID)
	if err != nil {
		return work.Run{}, err
	}
	if input.ConversationID != c.Task.ConversationID || input.AgentID != c.Task.AgentID {
		return work.Run{}, work.ErrState
	}
	if err := os.MkdirAll(filepath.Dir(p.Workdir), 0700); err != nil {
		return work.Run{}, err
	}
	// A forge merge advances the remote, not the clone's checked-out branch.
	// Fetch a tracking ref, resolve one immutable commit, and keep local refs intact.
	remoteRef := "refs/remotes/origin/" + repo.Base
	if _, err := d.command(ctx, d.config.Git, repo.Clone, "fetch", "--no-tags", "--no-write-fetch-head", "origin", "+refs/heads/"+repo.Base+":"+remoteRef); err != nil {
		return work.Run{}, err
	}
	rawBase, err := d.command(ctx, d.config.Git, repo.Clone, "rev-parse", "--verify", remoteRef+"^{commit}")
	if err != nil {
		return work.Run{}, err
	}
	base := strings.TrimSpace(string(rawBase))
	if !gitObject.MatchString(base) {
		return work.Run{}, work.ErrState
	}
	for _, id := range p.DependsOn {
		predecessor, err := d.root.work.Get(ctx, id)
		if err != nil {
			return work.Run{}, err
		}
		observed := predecessor.Scheduling.PR
		if !predecessor.Scheduling.Landed || observed == nil || !gitObject.MatchString(observed.MergeCommit) {
			return work.Run{}, work.ErrState
		}
		if _, err = d.command(ctx, d.config.Git, repo.Clone, "merge-base", "--is-ancestor", observed.MergeCommit, base); err != nil {
			return work.Run{}, err
		}
	}
	// The task id is Core-minted; git refuses any pre-existing branch/worktree.
	if _, err := d.command(ctx, d.config.Git, repo.Clone, "worktree", "add", "-b", "lina/"+c.Task.ID, p.Workdir, base); err != nil {
		return work.Run{}, err
	}
	l, err := d.root.activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "delegated worker turn"})
	if err != nil {
		return work.Run{}, err
	}
	d.mu.Lock()
	d.leases["pending:"+c.Task.ID] = l
	d.mu.Unlock()
	process, err := d.root.activities.Acquire(d.ctx, lease.Activity{Class: lease.Reported, Reason: "delegated Codex session"})
	if err != nil {
		return work.Run{}, err
	}
	adapter, err := codex.Open(d.ctx, process, codex.Config{Executable: d.config.Codex})
	if err != nil {
		process.Release()
		return work.Run{}, err
	}
	d.mu.Lock()
	d.adapters[c.Task.ID] = adapter
	d.processLeases = append(d.processLeases, process)
	d.mu.Unlock()

	// After this boundary a failed response can conceal an accepted start.
	sent = true
	thread, err := adapter.Start(ctx, l, worker.StartSpec{TaskID: c.Task.ID, AttemptID: c.Task.ID + ":1", Workdir: p.Workdir, TaskPacket: p.Packet, Tools: workerWorkTools(), Required: worker.CapabilitySet{worker.StreamProgress, worker.SignalCompletion, worker.WorkTools, worker.RequestApprovals, worker.ReportQuota}})
	if err != nil {
		return work.Run{}, err
	}
	d.mu.Lock()
	d.threads[c.Task.ID] = thread
	delete(d.leases, "pending:"+c.Task.ID)
	d.leases[thread.ID+":"+thread.TurnID] = l
	d.mu.Unlock()
	entry := &workerSession{scheduled: true, adapter: adapter, close: adapter.Close, compatibility: adapter.Compatibility(), origin: origin, input: input, thread: thread, packet: p.Packet, workdir: p.Workdir, approvals: map[string]worker.Event{}, questions: map[string]*workerQuestion{}, active: map[string]lease.Lease{}, done: make(chan struct{}), process: process}
	entry.answered = func(answerCtx context.Context, t worker.Thread) {
		_ = d.reactor.Answered(answerCtx, t.TaskID, t.ID, t.TurnID)
	}
	d.root.workers.mu.Lock()
	if d.root.workers.closed || d.root.workers.sessions[thread.ID] != nil {
		d.root.workers.mu.Unlock()
		return work.Run{}, work.ErrState
	}
	d.root.workers.sessions[thread.ID] = entry
	d.root.workers.mu.Unlock()
	// Events stay buffered until Start binds the returned thread and turn.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer close(entry.done)
		defer entry.stopRequests("")
		for {
			select {
			case e, ok := <-adapter.Events():
				if !ok {
					return
				}
				select {
				case d.workerEvents <- e:
				case <-d.ctx.Done():
					return
				}
			case <-adapter.QuotaUpdates():
				select {
				case d.quotaChanged <- struct{}{}:
				default:
				}
			case <-d.ctx.Done():
				return
			}
		}
	}()

	return work.Run{ID: c.Task.ID + ":1", TaskID: c.Task.ID, Generation: 1, Worker: "codex", ThreadID: thread.ID, TurnID: thread.TurnID, NodeID: "local"}, nil
}
func (d *delegationRuntime) returnConflict(ctx context.Context, c work.Card) (work.Run, error) {
	d.mu.Lock()
	thread, ok := d.threads[c.Task.ID]
	adapter := d.adapters[c.Task.ID]
	d.mu.Unlock()
	if !ok {
		return work.Run{}, errors.New("worker requires reconciliation")
	}
	l, err := d.root.activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "worker conflict correction"})
	if err != nil {
		return work.Run{}, err
	}
	d.mu.Lock()
	d.leases["pending:"+c.Task.ID] = l
	d.mu.Unlock()
	if err = adapter.Input(ctx, thread, worker.Input{Text: "Your pull request is unmergeable after another landing. Resolve the conflict on your existing branch, run the relevant checks, and update the same pull request. This is the single automatic conflict return; do not merge it."}); err != nil {
		return work.Run{}, err
	}
	snapshot, err := adapter.Snapshot(ctx, thread)
	if err != nil {
		return work.Run{}, err
	}
	thread = snapshot.Thread
	d.mu.Lock()
	d.threads[c.Task.ID] = thread
	delete(d.leases, "pending:"+c.Task.ID)
	d.leases[thread.ID+":"+thread.TurnID] = l
	d.mu.Unlock()
	run := *c.Run
	run.Generation++
	run.ID = c.Task.ID + ":" + strconv.Itoa(run.Generation)
	run.TurnID = thread.TurnID
	return run, nil
}
func (d *delegationRuntime) readPR(ctx context.Context, c work.Card) (work.PullRequest, error) {
	var result struct {
		Number      int    `json:"number"`
		Head        string `json:"headRefOid"`
		HeadRef     string `json:"headRefName"`
		Base        string `json:"baseRefName"`
		State       string `json:"state"`
		Mergeable   string `json:"mergeable"`
		MergeCommit *struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	repo := d.config.Repositories[c.Task.Schedule.Repository]
	raw, err := d.command(ctx, d.config.GH, repo.Clone, "pr", "view", "lina/"+c.Task.ID, "--repo", c.Task.Schedule.Repository, "--json", "number,headRefOid,headRefName,baseRefName,state,mergeable,mergeCommit")
	if err != nil {
		return work.PullRequest{}, err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return work.PullRequest{}, err
	}
	if result.HeadRef != "lina/"+c.Task.ID || result.Base != repo.Base {
		return work.PullRequest{}, work.ErrState
	}
	pr := work.PullRequest{Number: result.Number, Head: result.Head, State: result.State, Mergeable: result.Mergeable}
	if result.MergeCommit != nil {
		pr.MergeCommit = result.MergeCommit.OID
	}
	return pr, nil
}
func (d *delegationRuntime) events(ctx context.Context) {
	defer close(d.done)
	// Five-minute reconciliation catches a lost forge event; ordinary release is
	// driven by completion, submission, quota notifications and reset deadlines.
	sweep := time.NewTicker(5 * time.Minute)
	defer sweep.Stop()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		d.mu.Lock()
		reset := d.reset
		d.mu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var resetWake <-chan time.Time
		if !reset.IsZero() {
			timer.Reset(max(time.Until(reset), time.Millisecond))
			resetWake = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case event := <-d.workerEvents:
			if event.Validate() != nil {
				continue
			}
			switch event.Kind {
			case worker.FinishEvent:
				if event.Finish.Failure == worker.ConnectionFailure && event.Finish.Code == "connection_lost" {
					_ = d.reactor.Unknown(ctx, event.Thread.TaskID, event.Thread.ID, event.Thread.TurnID)
					continue
				}
				status := "failed"
				switch event.Finish.Status {
				case worker.Done:
					status = "completed"
				case worker.Interrupted:
					status = "interrupted"
				case worker.Failed:
				}
				_ = d.reactor.Completed(ctx, event.Thread.TaskID, event.Thread.ID, event.Thread.TurnID, status)
				// Late duplicates can release only their own turn's activity.
				key := event.Thread.ID + ":" + event.Thread.TurnID
				d.mu.Lock()
				activity := d.leases[key]
				delete(d.leases, key)
				d.mu.Unlock()
				activity.Release()
				if d.root.workers != nil {
					d.root.workers.mu.Lock()
					entry := d.root.workers.sessions[event.Thread.ID]
					d.root.workers.mu.Unlock()
					if entry != nil {
						entry.stopRequests(event.Thread.TurnID)
					}
				}
			case worker.QuestionEvent, worker.ApprovalEvent:
				if err := d.reactor.InputNeeded(ctx, event.Thread.TaskID, event.Thread.ID, event.Thread.TurnID); err != nil {
					continue
				}
				d.root.workers.mu.Lock()
				entry := d.root.workers.sessions[event.Thread.ID]
				d.root.workers.mu.Unlock()
				if entry != nil {
					if event.Kind == worker.QuestionEvent {
						entry.requestQuestion(event)
					} else {
						entry.requestApproval(event)
					}
				}
			case worker.ToolCallEvent:
				if event.ToolCall.Name == "ask" {
					if err := d.reactor.InputNeeded(ctx, event.Thread.TaskID, event.Thread.ID, event.Thread.TurnID); err != nil {
						continue
					}
				}
				d.root.workers.mu.Lock()
				entry := d.root.workers.sessions[event.Thread.ID]
				d.root.workers.mu.Unlock()
				if entry != nil {
					entry.workTool(event)
				}
			case worker.ProgressEvent, worker.UsageEvent:
			}
		case <-d.resetChanged:
			continue
		case <-d.quotaChanged:
			_ = d.reactor.Wake(ctx)
		case <-d.adapter.QuotaUpdates():
			_ = d.reactor.Wake(ctx)
		case <-resetWake:
			_ = d.reactor.Wake(ctx)
		case <-sweep.C:
			cards, err := d.root.work.Cards(ctx)
			if err == nil {
				for _, c := range cards {
					if c.Task.Schedule != nil && !c.Active {
						_ = d.reactor.Observe(ctx, c.Task.ID)
					}
				}
			}
		}
	}
}
func delegatedID(turn, call, key string) string {
	sum := sha256.Sum256([]byte(turn + "\x00" + call + "\x00" + key))
	return "task_" + hex.EncodeToString(sum[:12])
}
