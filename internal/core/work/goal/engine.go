// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var errNoChange = errors.New("work_no_change")
var ErrNeedsPerson = errors.New("work_needs_person")

// Driver is a trusted integration boundary, never model-provided code. It must
// recheck authority and exact target at dispatch, and return observed evidence,
// not a command exit code. Execute honors the durable action id as its receipt
// key. Observe is read-only and must never repeat an uncertain effect.
type Driver interface {
	Authorize(context.Context, Action) error
	Execute(context.Context, Action) (Observation, error)
	Observe(context.Context, Action) (Observation, error)
}

type Outcome string

const (
	OutcomeRunning     Outcome = "running"
	OutcomeSucceeded   Outcome = "succeeded"
	OutcomeFailed      Outcome = "failed"
	OutcomeConflict    Outcome = "conflict"
	OutcomeUnknown     Outcome = "unknown"
	OutcomeNeedsPerson Outcome = "needs_person"
	OutcomeQuota       Outcome = "quota"
	OutcomeNoReceipt   Outcome = "no_receipt"
)

type Observation struct {
	QuotaReset     int64              `json:"quota_reset,omitempty"`
	Criterion      *CriterionEvidence `json:"criterion,omitempty"`
	Tasks          []Task             `json:"tasks,omitempty"`
	Status         Outcome            `json:"status"`
	Handle         string             `json:"handle,omitempty"`
	Head           string             `json:"head,omitempty"`
	Proof          Proof              `json:"proof"`
	PR             PRState            `json:"pr"`
	EffectsSettled bool               `json:"effects_settled"`
	Code           string             `json:"code,omitempty"`
}
type Config struct {
	Records       Definitions
	DriverFactory func(*Engine) (Driver, error)
	AuthorizeGoal func(context.Context, Goal) error
	Refresh       func(context.Context, *Engine) error
	DB            Canon
	Epoch         uint64
	Now           func() time.Time
	Driver        Driver
	Sweep         <-chan time.Time
	// Changed publishes a committed projection. A delivery failure does not
	// revoke the state; startup and subsequent events can publish it again.
	Changed func(context.Context, State) error
}
type Engine struct {
	definitions sync.Map
	journal     *Journal
	cfg         Config
	mu          sync.Mutex
	running     map[string]bool
	wg          sync.WaitGroup
	failures    chan error
}

func NewEngine(cfg Config) (*Engine, error) {
	if cfg.Records == nil || cfg.DB == nil || cfg.Epoch == 0 {
		return nil, ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	e := &Engine{journal: NewJournal(cfg.DB), cfg: cfg, running: map[string]bool{}, failures: make(chan error, 1)}
	if cfg.DriverFactory != nil {
		if cfg.Driver != nil {
			return nil, ErrInvalid
		}
		driver, err := cfg.DriverFactory(e)
		if err != nil {
			return nil, err
		}
		if driver == nil {
			return nil, ErrInvalid
		}
		e.cfg.Driver = driver
	}
	return e, nil
}
func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func eventFor(kind, key string, data any, at int64) Transition {
	raw := encode(data)
	sum := sha256.Sum256(raw)
	return Transition{ID: kind + ":" + key + ":" + hex.EncodeToString(sum[:]), Kind: kind, At: at, Data: raw}
}
func put(v *View, kind, id string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return v.Put(kind, id, raw)
}
func snapshotView(v *View) (State, error) {
	var s State
	for _, r := range v.Records("goal") {
		var x Goal
		if err := json.Unmarshal(r.Body, &x); err != nil {
			return s, err
		}
		s.Goals = append(s.Goals, x)
	}
	for _, r := range v.Records("node") {
		var x Node
		if err := json.Unmarshal(r.Body, &x); err != nil {
			return s, err
		}
		s.Nodes = append(s.Nodes, x)
	}
	for _, r := range v.Records("action") {
		var x Action
		if err := json.Unmarshal(r.Body, &x); err != nil {
			return s, err
		}
		s.Actions = append(s.Actions, x)
	}
	return s, nil
}
func (e *Engine) Snapshot(ctx context.Context) (State, error) {
	var s State
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		v, err := readView(ctx, tx)
		if err == nil {
			s, err = snapshotView(v)
		}
		return err
	})
	if err == nil {
		for i := range s.Goals {
			g := &s.Goals[i]
			cached, ok := e.definitions.Load(definitionKey{g.RecordID, g.RecordRevision})
			if !ok || cached.(savedDefinition).revision != g.RecordRevision {
				r, rev, readErr := e.cfg.Records.Read(ctx, g.RecordID)
				if readErr != nil {
					return s, readErr
				}
				d, readErr := parseDefinition(r, g.Person, g.Agent)
				if readErr != nil {
					return s, readErr
				}
				e.definitions.Store(definitionKey{g.RecordID, rev}, savedDefinition{d, rev, string(r.Data)})
			}
			if err = e.hydrate(g); err != nil {
				return s, err
			}
		}
	}
	return s, err
}
func loadGoal(v *View, id string) (Goal, error) {
	var g Goal
	r, ok := v.Get("goal", id)
	if !ok {
		return g, ErrInvalid
	}
	err := json.Unmarshal(r.Body, &g)
	return g, err
}
func loadAction(v *View, id string) (Action, error) {
	var a Action
	r, ok := v.Get("action", id)
	if !ok {
		return a, ErrInvalid
	}
	err := json.Unmarshal(r.Body, &a)
	return a, err
}
func goalTask(g *Goal, id string) *Task {
	for i := range g.Tasks {
		if g.Tasks[i].ID == id {
			return &g.Tasks[i]
		}
	}
	return nil
}

// A goal revision invalidates undispatched actions, including their reservations and
// one-shot planning/recovery counters. They have never been dispatched. Claimed
// actions retain their receipts and reservations until their effects settle.
func retireUndispatchedActions(v *View, g *Goal) error {
	for _, r := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(r.Body, &a); err != nil {
			return err
		}
		if a.GoalID != g.ID || a.ObserveActionID != "" || (a.State != "queued" && a.State != "awaiting_authorization") || a.GoalRevision == g.Revision {
			continue
		}
		restoreUndispatchedTask(g, a)
		a.State = "finished"
		a.Superseded, a.RetirementReconciled = true, true
		if err := put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}

// restoreUndispatchedTask refunds only work that never reached execution.
func restoreUndispatchedTask(g *Goal, a Action) {
	t := goalTask(g, a.TaskID)
	if t != nil {
		switch a.Kind {
		case Assign:
			t.Stage = TaskReady
			t.NodeID = ""
		case Resume:
			t.Stage = TaskWorkerLost
		case RepairConflict:
			t.Stage = TaskConflicted
		case ContinueWorker:
			t.Stage = TaskNoReceipt
		case RepairQA:
			t.Stage = TaskQAFailed
		case PlanGoal:
			t.RepairAsked = false
		case InspectPR, Merge, RunQA, Deploy, Verify, Observe, Decide, CheckCriterion:
		}
		if reserves(a) {
			refundRecovery(t, a.Kind)
			t.Attempt--
			t.EffectsSettled = true
		}
	}
}

// Cancellation before dispatch and a settled quota interruption both leave the
// task's one recovery available. A quota attempt still retains its attempt id.
func refundRecovery(t *Task, kind ActionKind) {
	switch kind {
	case Resume:
		t.Recoveries--
	case ContinueWorker:
		t.Continuations--
	case RepairConflict:
		t.Conflicts--
	case RepairQA:
		t.QARepairs--
	case Assign, InspectPR, Merge, RunQA, Deploy, Verify, Observe, Decide, PlanGoal, CheckCriterion:
	}
}

func (e *Engine) Admit(ctx context.Context, g Goal) error {
	if e.cfg.AuthorizeGoal != nil {
		if err := e.cfg.AuthorizeGoal(ctx, g); err != nil {
			return err
		}
	}
	if g.CreatedAt == 0 {
		g.CreatedAt = e.cfg.Now().Unix()
	}
	g.ServiceRepository = serviceRepository(g)
	if err := validateGoal(g); err != nil {
		return err
	}
	if g.DefinitionPending || g.AssessedFailure != "" || g.CheckRound != 0 || len(g.Checks) > 0 || g.State != "" || g.Revision != 0 || g.ManagerTurns != 0 || g.AttentionID != "" || g.BlockedReason != "" || len(g.Waits) > 0 || len(g.Evidence) > 0 || g.TargetRevision != "" || len(g.MergedRevisions) > 0 || g.Deployment != (Proof{}) || g.Verification != (Proof{}) {
		return ErrInvalid
	}
	for _, t := range g.Tasks {
		if !freshTask(t) || t.Repairs != "" {
			return ErrInvalid
		}
	}
	if err := e.prepareDefinition(ctx, &g); err != nil {
		return err
	}
	g.Criteria = goalCriteria(g)
	g.State = GoalRunning
	if len(g.Tasks) == 0 {
		g.State = GoalDefining
	}
	g.Revision = 1
	return e.journal.Update(ctx, eventFor("goal.admitted", g.ID, g, e.cfg.Now().Unix()), func(v *View) error {
		if _, ok := v.Get("goal", g.ID); ok {
			return ErrConflict
		}
		return put(v, "goal", g.ID, g)
	})
}
func (e *Engine) NodeChanged(ctx context.Context, n Node) error {
	if n.ID == "" || n.ObservedAt <= 0 || n.ObservedAt > e.cfg.Now().Unix() || !finite(n.CPUCores) || !finite(n.LoadCPU) || !finite(n.CPULimit) {
		return ErrInvalid
	}
	return e.journal.Update(ctx, eventFor("node.changed", n.ID, n, n.ObservedAt), func(v *View) error {
		if r, ok := v.Get("node", n.ID); ok {
			var previous Node
			if err := json.Unmarshal(r.Body, &previous); err != nil {
				return err
			}
			if previous.ObservedAt > n.ObservedAt {
				return ErrConflict
			}
			if n.QuotaReset == nil {
				n.QuotaReset = map[string]int64{}
			}
			for worker, reset := range previous.QuotaReset {
				if reset > e.cfg.Now().Unix() && reset > n.QuotaReset[worker] {
					n.QuotaReset[worker] = reset
				}
			}
		}
		return put(v, "node", n.ID, n)
	})
}

func (e *Engine) Reconcile(ctx context.Context) error {
	if err := e.RefreshDefinitions(ctx); err != nil {
		return err
	}
	now := e.cfg.Now()
	err := e.journal.Update(ctx, Transition{ID: "derive:" + rand.Text(), Kind: "work.derived", At: now.Unix()}, func(v *View) error {
		if err := e.reconcileRetired(v); err != nil {
			return err
		}
		s, err := snapshotView(v)
		if err != nil {
			return err
		}
		for i := range s.Goals {
			if err = e.hydrate(&s.Goals[i]); err != nil {
				return err
			}
		}
		p, err := Derive(s, now)
		if err != nil {
			return err
		}
		for _, planned := range p.Actions {
			a := planned
			for _, old := range s.Actions {
				if old.GoalID == a.GoalID && old.TaskID == a.TaskID && old.Kind == a.Kind && old.Attempt == a.Attempt && old.Revision == a.Revision && old.CriterionID == a.CriterionID && old.GoalRevision == a.GoalRevision && old.CheckRound == a.CheckRound {
					a.Sequence = max(a.Sequence, old.Sequence+1)
				}
			}
			a.ID = actionID(a)
			if err = put(v, "action", a.ID, a); err != nil {
				return err
			}
			for gi := range s.Goals {
				g := &s.Goals[gi]
				if g.ID != a.GoalID {
					continue
				}
				if a.Kind == CheckCriterion {
					g.CheckRound = a.CheckRound
				}
				t := goalTask(g, a.TaskID)
				if t != nil && a.Kind == PlanGoal {
					t.RepairAsked = true
				}
				if t != nil && reserves(a) && a.ObserveActionID == "" {
					t.Stage = TaskBuilding
					t.Attempt = a.Attempt
					t.NodeID = a.NodeID
					t.PR = PRState{}
					t.EffectsSettled = false
					if a.Kind == ContinueWorker {
						t.Continuations++
					}
					if a.Kind == Resume {
						t.Recoveries++
					}
					if a.Kind == RepairConflict {
						t.Conflicts++
					}
					if a.Kind == RepairQA {
						t.QARepairs++
					}
				}
			}
		}
		for i := range s.Goals {
			g := &s.Goals[i]
			waits := []Wait{}
			for _, w := range p.Waits {
				if w.GoalID != g.ID {
					continue
				}
				for _, old := range g.Waits {
					if old.TaskID == w.TaskID && old.Reason == w.Reason && old.Wake == w.Wake {
						w.Until = old.Until
						break
					}
				}
				waits = append(waits, w)
			}
			g.Waits = waits
			if g.State != GoalDone && g.State != GoalStopped {
				allLanded := len(g.Tasks) > 0
				for _, t := range g.Tasks {
					allLanded = allLanded && t.Stage == TaskDone
				}
				active, person := false, false
				for _, r := range v.Records("action") {
					var a Action
					if err := json.Unmarshal(r.Body, &a); err != nil {
						return err
					}
					if a.GoalID != g.ID || a.State == "finished" {
						continue
					}
					if a.State == "needs_person" || a.State == "awaiting_authorization" {
						person = true
					} else {
						active = true
					}
				}
				for _, w := range waits {
					person = person || w.Reason == "person"
				}
				switch {
				case g.DefinitionPending && g.AssessedFailure == "definition:"+g.RecordRevision:
					g.State = GoalBlocked
				case has(p.Close, g.ID):
					g.State = GoalDone
				case has(p.Block, g.ID) || person:
					g.State = GoalBlocked
					g.AttentionID = fmt.Sprintf("goal:%s:%d:attention", g.ID, g.Revision)
					g.BlockedReason = "LINA could not find a way to finish the remaining done condition; a decision is needed."
				case g.DefinitionPending:
					g.State = GoalRunning
				case allLanded:
					g.State = GoalChecking
				case active:
					g.State = GoalRunning
				case len(waits) > 0:
					g.State = GoalWaiting
				default:
					g.State = GoalDefining
				}
			}
			if err = put(v, "goal", g.ID, g); err != nil {
				return err
			}
		}
		if len(v.changes) == 0 {
			return errNoChange
		}
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

// Claim is durable before any authorization or external dispatch. A claimed
// action after a crash is observed, never returned to the execution queue.
func (e *Engine) Claim(ctx context.Context, id string) (Action, error) {
	var out Action
	err := e.journal.Update(ctx, Transition{ID: "claim:" + rand.Text(), Kind: "action.claim_checked", At: e.cfg.Now().Unix()}, func(v *View) error {
		a, err := loadAction(v, id)
		if err != nil {
			return err
		}
		if a.State != "queued" {
			return ErrConflict
		}
		g, err := loadGoal(v, a.GoalID)
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		if g.State == GoalDone || g.State == GoalStopped {
			return ErrConflict
		}
		if changedInputs(g, a) || a.ObserveActionID == "" && !currentActionTarget(g, a) {
			return ErrConflict
		}
		if g.DefinitionPending && a.Kind != PlanGoal && a.ObserveActionID == "" {
			return ErrConflict
		}
		if reserves(a) && a.ObserveActionID == "" {
			s, err := snapshotView(v)
			if err != nil {
				return err
			}
			t := goalTask(&g, a.TaskID)
			if t == nil {
				return ErrInvalid
			}
			// The award already owns one reservation. Count every other
			// reservation, including queued siblings, against the latest report.
			others := make([]Action, 0, len(s.Actions))
			for _, other := range s.Actions {
				if other.ID != a.ID {
					others = append(others, other)
				}
			}
			eligible := false
			now := e.cfg.Now()
			for _, n := range s.Nodes {
				if n.ID == a.NodeID && n.Workers[t.Worker] && n.QuotaReset[t.Worker] <= now.Unix() && Slots(n, others, a.Resources, now) > 0 {
					eligible = true
					break
				}
			}
			if !eligible {
				restoreUndispatchedTask(&g, a)
				a.State = "finished"
				if err = put(v, "action", a.ID, a); err != nil {
					return err
				}
				// Commit the release before returning a claim conflict. The
				// journal wake derives a new award or a named capacity wait.
				return put(v, "goal", g.ID, g)
			}
		}
		a.State = "claimed"
		a.Epoch = e.cfg.Epoch
		a.InputVersion = inputVersion(g, a)
		out = a
		return put(v, "action", id, a)
	})
	if err == nil && out.ID == "" {
		err = ErrConflict
	}
	return out, err
}

func (e *Engine) Report(ctx context.Context, id string, epoch uint64, o Observation) error {
	if epoch != e.cfg.Epoch {
		return ErrConflict
	}
	now := e.cfg.Now()
	// Delivery is deduplicated against the receipted observation inside the
	// transaction. Redelivery may have a different local receive timestamp.
	ev := eventFor("action.observed", id, struct {
		Epoch       uint64
		Observation Observation
	}{epoch, o}, now.Unix())
	ev.ID = "action.observed:" + id + ":" + rand.Text()
	err := e.report(ctx, ev, id, o)
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}
func (e *Engine) report(ctx context.Context, ev Transition, id string, o Observation) error {
	return e.journal.Update(ctx, ev, func(v *View) error {
		a, err := loadAction(v, id)
		if err != nil {
			return err
		}
		if a.State == "finished" && bytes.Equal(a.Receipt, encode(o)) {
			return errNoChange
		}
		if a.State == "queued" {
			return ErrConflict
		}
		g, err := loadGoal(v, a.GoalID)
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		t := goalTask(&g, a.TaskID)
		previousRevision := g.Revision
		a.ObservedAt = e.cfg.Now().Unix()
		a.ObserverEpoch = e.cfg.Epoch
		a.Receipt = encode(o)
		a.Deadline = e.cfg.Now().Add(SafetySweep).Unix()
		if staleInputs(g, a) {
			// Retain the obsolete response as history, never as current evidence
			// or a plan for this revision. A late result is not a Core failure.
			a.State = "finished"
			a.Superseded = true
			// Only reconcileRetired clears assessment markers. A late report
			// for an already reconciled action must not clear a fresh assessment.
			if hasExternalEffect(a) && !currentActionTarget(g, a) && !effectSettled(a, o) {
				// A different target cannot inherit this effect. Retain its
				// lease and bounded observation wait until its outcome is known.
				a.State = "unknown"
				if o.Handle != "" {
					a.Handle = o.Handle
				}
			}
			return put(v, "action", id, a)
		}
		if a.State == "finished" {
			return ErrConflict
		}
		switch o.Status {
		case OutcomeQuota:
			if t == nil || !reserves(a) || !o.EffectsSettled || o.QuotaReset <= e.cfg.Now().Unix() {
				return ErrInvalid
			}
			r, ok := v.Get("node", a.NodeID)
			if !ok {
				return ErrInvalid
			}
			var n Node
			if err = json.Unmarshal(r.Body, &n); err != nil {
				return err
			}
			if n.QuotaReset == nil {
				n.QuotaReset = map[string]int64{}
			}
			n.QuotaReset[t.Worker] = o.QuotaReset
			if err = put(v, "node", n.ID, n); err != nil {
				return err
			}
			a.State = "finished"
			refundRecovery(t, a.Kind)
			t.Stage = TaskReady
			t.NodeID = ""
			t.EffectsSettled = true
		case OutcomeNoReceipt:
			if t == nil || !reserves(a) || o.Handle == "" {
				return ErrInvalid
			}
			if !o.EffectsSettled {
				a.State = "unknown"
				break
			}
			a.State = "finished"
			t.ThreadHandle = o.Handle
			t.EffectsSettled = true
			if t.Continuations == 0 {
				t.Stage = TaskNoReceipt
			} else {
				t.Stage = TaskWorkerLost
			}
		case OutcomeRunning:
			if o.Handle == "" {
				return ErrInvalid
			}
			a.State = "running"
			a.Handle = o.Handle
		case OutcomeUnknown:
			a.State = "unknown"
		case OutcomeNeedsPerson:
			a.State = "needs_person"
			if o.Handle != "" {
				a.Handle = o.Handle
			}
			if t != nil && reserves(a) {
				t.EffectsSettled = o.EffectsSettled
			}
		case OutcomeConflict:
			if t == nil || a.Kind != Merge && a.Kind != InspectPR {
				return ErrInvalid
			}
			if a.Kind == InspectPR {
				// Only an explicit conflict for the inspected current head spends
				// the existing rebase allowance. Mergeable=false alone is unknown.
				if o.PR.Head == "" || o.PR.Head != a.Revision || o.PR.Head != t.Head || o.PR.Mergeable {
					return ErrInvalid
				}
				t.PR = o.PR
				t.PR.ObservedAt = a.ObservedAt
			}
			a.State = "finished"
			t.Stage = TaskConflicted
		case OutcomeFailed:
			if a.Kind == Verify {
				if t != nil || !o.Proof.fails(a.Revision) {
					return ErrInvalid
				}
				if g.TargetRevision != a.Revision || g.Revision != a.GoalRevision {
					return ErrConflict
				}
				a.State = "finished"
				g.Verification = o.Proof
				g.Evidence = append(g.Evidence, o.Proof)
				break
			}
			if t == nil {
				a.State = "needs_person"
				break
			}
			switch a.Kind {
			case Assign, Resume, RepairConflict, RepairQA, ContinueWorker:
				if !o.EffectsSettled {
					a.State = "unknown"
					break
				}
				a.State = "finished"
				t.Stage = TaskWorkerLost
				t.EffectsSettled = true
			case RunQA:
				if o.Proof.Ref == "" || o.Proof.Revision != a.Revision || o.Proof.Passed {
					return ErrInvalid
				}
				a.State = "finished"
				t.Stage = TaskQAFailed
				t.QA = o.Proof
				g.Evidence = append(g.Evidence, o.Proof)
			case InspectPR, Merge, PlanGoal:
				a.State = "needs_person"
			case Deploy, Verify, Observe, Decide, CheckCriterion:
				return ErrInvalid
			}
		case OutcomeSucceeded:
			a.State = "finished"
			if err = applySuccess(&g, t, a, o, e.cfg.Now().Unix()); err != nil {
				return err
			}
		default:
			return ErrInvalid
		}
		if g.Revision != previousRevision {
			if err = retireUndispatchedActions(v, &g); err != nil {
				return err
			}
		}
		if err = put(v, "goal", g.ID, g); err != nil {
			return err
		}
		return put(v, "action", id, a)
	})
}

func applySuccess(g *Goal, t *Task, a Action, o Observation, now int64) error {
	switch a.Kind {
	case CheckCriterion:
		if t != nil || a.Check == nil || o.Criterion == nil || o.Criterion.Criterion != a.CriterionID || o.Criterion.GoalRevision != a.GoalRevision || o.Criterion.Round != a.CheckRound {
			return ErrInvalid
		}
		var criterion Criterion
		for _, c := range goalCriteria(*g) {
			if c.ID == a.CriterionID {
				criterion = c
				break
			}
		}
		if !currentEvidence(*g, criterion, *o.Criterion) || o.Criterion.Revision != a.Revision {
			return ErrConflict
		}
		for i, old := range g.Checks {
			if old.Criterion == a.CriterionID {
				g.Checks[i] = *o.Criterion
				return nil
			}
		}
		g.Checks = append(g.Checks, *o.Criterion)
	case PlanGoal:
		if g.DefinitionPending {
			return applyCorrectionPlan(g, o)
		}
		if t != nil && (t.Stage != TaskQAFailed || t.RepairedBy != "") {
			return ErrInvalid
		}
		if t == nil {
			g.ManagerTurns++
		}
		if a.FailureKey != "" {
			g.AssessedFailure = a.FailureKey
		}
		if len(o.Tasks) > 0 {
			for _, task := range o.Tasks {
				if !freshTask(task) {
					return ErrInvalid
				}
				if t != nil && (len(o.Tasks) != 1 || task.Repairs != t.ID || task.Repository != t.Repository || task.Base != t.Base) {
					return ErrInvalid
				}
				if t == nil && task.Repairs != "" {
					return ErrInvalid
				}
			}
			if t != nil {
				t.RepairedBy = o.Tasks[0].ID
			}
			g.Tasks = append(g.Tasks, o.Tasks...)
			g.Revision++
			g.Criteria = goalCriteria(*g)
			if err := validateGoal(*g); err != nil {
				return err
			}
		}

	case Assign, Resume, RepairConflict, RepairQA, ContinueWorker:
		if t == nil || o.Head == "" || !o.EffectsSettled {
			return ErrInvalid
		}
		t.Head = o.Head
		t.Stage = TaskDelivered
		t.EffectsSettled = true
	case InspectPR:
		if t == nil || o.PR.Head == "" {
			return ErrInvalid
		}
		t.Head = o.PR.Head
		t.PR = o.PR
		t.PR.ObservedAt = now
	case Merge:
		if t == nil || o.Head != a.Revision || !o.Proof.proves(o.Proof.Revision) {
			return ErrInvalid
		}
		t.Merge = o.Proof
		t.QA = Proof{}
		t.Stage = TaskMerged
		if g.MergedRevisions == nil {
			g.MergedRevisions = map[string]string{}
		}
		g.MergedRevisions[t.Repository] = o.Proof.Revision
		previous := g.TargetRevision
		if g.ServiceTarget == "" {
			g.TargetRevision = o.Proof.Revision
		} else {
			g.TargetRevision = targetRevision(*g)
		}
		if previous != g.TargetRevision {
			g.Deployment = Proof{}
		}
		g.Verification = Proof{}
		g.Evidence = append(g.Evidence, o.Proof)
	case RunQA:
		if t == nil || !o.Proof.proves(a.Revision) || t.Merge.Revision != a.Revision {
			return ErrInvalid
		}
		t.QA = o.Proof
		t.ResultRevision = o.Proof.Revision
		t.Stage = TaskDone
		repaired := t.ID
		for range len(g.Tasks) {
			parent := ""
			for i := range g.Tasks {
				original := &g.Tasks[i]
				if original.RepairedBy == repaired {
					original.QA = o.Proof
					original.ResultRevision = o.Proof.Revision
					original.Stage = TaskDone
					parent = original.ID
					break
				}
			}
			if parent == "" {
				break
			}
			repaired = parent
		}
		g.Evidence = append(g.Evidence, o.Proof)
	case Deploy:
		if t != nil || g.ServiceTarget == "" || !o.Proof.proves(a.Revision) {
			return ErrInvalid
		}
		g.Evidence = append(g.Evidence, o.Proof)
		// A settled old deployment remains history, but a corrected service
		// binding needs fresh evidence even when both repositories share a SHA.
		if a.GoalRevision == g.Revision && g.TargetRevision == a.Revision {
			g.Deployment = o.Proof
		}
	case Verify:
		if t != nil || !o.Proof.proves(a.Revision) || g.TargetRevision != a.Revision {
			return ErrInvalid
		}
		g.Verification = o.Proof
		g.CheckRound++
		g.Checks = nil
		g.Evidence = append(g.Evidence, o.Proof)
	case Observe, Decide:
		return ErrInvalid
	}
	return nil
}

func (e *Engine) Run(ctx context.Context) error {
	if e.cfg.Driver == nil {
		return ErrInvalid
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := startReconciler(ctx, e.journal, func(ctx context.Context) error { return e.step(ctx) }, e.cfg.Sweep, func(ctx context.Context) error {
		if e.cfg.Refresh != nil {
			return e.cfg.Refresh(ctx, e)
		}
		return nil
	})
	var last error
	for {
		select {
		case err := <-e.failures:
			cancel()
			<-r.Done()
			e.wg.Wait()
			return err
		case err, ok := <-r.Errors():
			if ok {
				last = err
			}
		case <-r.Done():
			e.wg.Wait()
			return last
		}
	}
}

// Only a pre-dispatch authority refusal enters this state. Execution outcomes
// needing a person remain distinct: a change hint cannot safely replay them.
func (e *Engine) awaitAuthorization(ctx context.Context, claimed Action) error {
	return e.journal.Update(ctx, Transition{ID: "authorization.wait:" + rand.Text(), Kind: "authorization.waiting", At: e.cfg.Now().Unix()}, func(v *View) error {
		a, err := loadAction(v, claimed.ID)
		if err != nil {
			return err
		}
		if a.State != "claimed" || a.Epoch != e.cfg.Epoch {
			return ErrConflict
		}
		g, err := loadGoal(v, a.GoalID)
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		a.State = "awaiting_authorization"
		a.Receipt = encode(Observation{Status: OutcomeNeedsPerson, EffectsSettled: true})
		if a.Signal != claimed.Signal {
			// An approval hint arrived while Authorize was still reading. Do
			// not lose that wake; the next claim rechecks current authority.
			a.State = "queued"
		}
		// Authorize refused before Execute. A retry may therefore freeze fresh
		// inputs, but it must still pass authority again before any effect.
		if a.GoalRevision == g.Revision && currentActionTarget(g, a) {
			a.InputVersion = inputVersion(g, a)
		}
		if err = put(v, "action", a.ID, a); err != nil {
			return err
		}
		// A definition/repair revision can commit while Authorize runs.
		if err = retireUndispatchedActions(v, &g); err != nil {
			return err
		}
		return put(v, "goal", g.ID, g)
	})
}

func (e *Engine) releaseDispatch(id string) {
	e.mu.Lock()
	delete(e.running, id)
	e.mu.Unlock()
	// Reconciliation may have consumed an authorization hint while this slot
	// was still busy. Wake again after release so a queued retry runs now.
	e.journal.notify()
}

func (e *Engine) observationDue(a Action) bool {
	return (a.State == "claimed" || a.State == "running" || a.State == "unknown") && (a.Epoch != e.cfg.Epoch && a.ObserverEpoch != e.cfg.Epoch || a.Deadline <= e.cfg.Now().Unix())
}

// The previous dispatch may have finished or parked after step's snapshot but
// before it released the running reservation. Re-read after acquiring that
// reservation so an authorization retry is never observed as an executed effect.
func (e *Engine) readObservable(ctx context.Context, id string) (Action, error) {
	var a Action
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='action' AND id=?", id).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if !e.observationDue(a) {
			return ErrConflict
		}
		return nil
	})
	return a, err
}

func (e *Engine) step(ctx context.Context) error {
	if err := e.Reconcile(ctx); err != nil {
		return err
	}
	s, err := e.Snapshot(ctx)
	if err != nil {
		return err
	}
	if e.cfg.Changed != nil {
		if err := e.cfg.Changed(ctx, s); err != nil {
			return err
		}
	}
	for _, a := range s.Actions {
		execute := a.State == "queued"
		observe := e.observationDue(a)
		if !execute && !observe {
			continue
		}
		e.mu.Lock()
		if e.running[a.ID] {
			e.mu.Unlock()
			continue
		}
		e.running[a.ID] = true
		e.mu.Unlock()
		var current Action
		if execute {
			current, err = e.Claim(ctx, a.ID)
		} else {
			current, err = e.readObservable(ctx, a.ID)
		}
		if err != nil {
			e.mu.Lock()
			delete(e.running, a.ID)
			e.mu.Unlock()
			if errors.Is(err, ErrConflict) {
				continue
			}
			return err
		}
		a = current
		e.wg.Go(func() {
			defer e.releaseDispatch(a.ID)
			var o Observation
			var err error
			authorizationDenied := false
			effectMayHaveStarted := observe || a.ObserveActionID != ""
			if effectMayHaveStarted {
				source := a
				if a.ObserveActionID != "" {
					source.ID = a.ObserveActionID
				}
				o, err = e.cfg.Driver.Observe(ctx, source)
			} else if err = e.cfg.Driver.Authorize(ctx, a); err == nil {
				effectMayHaveStarted = true
				o, err = e.cfg.Driver.Execute(ctx, a)
			} else if errors.Is(err, ErrNeedsPerson) {
				authorizationDenied = true
			}
			if errors.Is(err, ErrNeedsPerson) {
				o = Observation{Status: OutcomeNeedsPerson, EffectsSettled: !effectMayHaveStarted}
			} else if err != nil {
				o = Observation{Status: OutcomeUnknown}
			}
			// Cancellation can leave a dispatched effect uncertain. The durable claim
			// survives; only an active generation may acknowledge the observation.
			if ctx.Err() == nil {
				if authorizationDenied {
					err = e.awaitAuthorization(ctx, a)
				} else {
					err = e.Report(ctx, a.ID, e.cfg.Epoch, o)
				}
				if err != nil && ctx.Err() == nil {
					select {
					case e.failures <- err:
					default:
					}
				}
			}
		})
	}
	return nil
}

// Summary is evidence, not an inferred status from the last worker message.
func (e *Engine) Summary(ctx context.Context, id string) (string, error) {
	s, err := e.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	for _, g := range s.Goals {
		if g.ID != id {
			continue
		}
		if g.State != GoalDone {
			return "", ErrConflict
		}
		text := g.Summary + ":"
		for _, t := range g.Tasks {
			text += fmt.Sprintf(" %s merged %s (%s), QA %s;", t.ID, t.Merge.Revision, t.Merge.Ref, t.QA.Ref)
		}
		if g.ServiceTarget != "" {
			text += " deployed " + g.Deployment.Revision + " (" + g.Deployment.Ref + ");"
		}
		text += " verified " + g.Verification.Ref
		return text, nil
	}
	return "", ErrInvalid
}
