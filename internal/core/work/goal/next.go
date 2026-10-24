// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"math"
	"sort"
	"strings"
	"time"
)

type Resources struct {
	CPU      float64 `json:"cpu"`
	MemoryMB int64   `json:"memory_mb"`
	DiskMB   int64   `json:"disk_mb"`
}
type Node struct {
	QuotaReset        map[string]int64 `json:"quota_reset,omitempty"`
	ID                string           `json:"id"`
	Connected         bool             `json:"connected"`
	Yielding          bool             `json:"yielding"`
	ObservedAt        int64            `json:"observed_at"`
	CPUCores          float64          `json:"cpu_cores"`
	CPULimit          float64          `json:"cpu_limit"`
	LoadCPU           float64          `json:"load_cpu"`
	AvailableMemoryMB int64            `json:"available_memory_mb"`
	MemoryLimitMB     int64            `json:"memory_limit_mb"`
	AvailableDiskMB   int64            `json:"available_disk_mb"`
	DiskLimitMB       int64            `json:"disk_limit_mb"`
	ForPerson         bool             `json:"for_person"`
	Workers           map[string]bool  `json:"workers"`
	// ObservedActions identifies reservations already included in this sample's
	// load/memory reading. Unacknowledged assignments are subtracted separately.
	ObservedActions []string `json:"observed_actions"`
}
type Proof struct {
	Revision string `json:"revision"`
	Ref      string `json:"ref"`
	Passed   bool   `json:"passed"`
}

func (p Proof) proves(revision string) bool {
	return revision != "" && p.Revision == revision && strings.TrimSpace(p.Ref) != "" && p.Passed
}

func (p Proof) fails(revision string) bool {
	return revision != "" && p.Revision == revision && strings.TrimSpace(p.Ref) != "" && !p.Passed
}

type PRState struct {
	Head       string `json:"head"`
	Checks     Proof  `json:"checks"`
	Review     Proof  `json:"review"`
	Verified   Proof  `json:"verified"`
	OpenP0P1   int    `json:"open_p0_p1"`
	Mergeable  bool   `json:"mergeable"`
	ObservedAt int64  `json:"observed_at"`
}

type TaskStage string

const (
	TaskReady       TaskStage = "ready"
	TaskBuilding    TaskStage = "building"
	TaskDelivered   TaskStage = "delivered"
	TaskConflicted  TaskStage = "conflicted"
	TaskWorkerLost  TaskStage = "worker_lost"
	TaskNoReceipt   TaskStage = "no_receipt"
	TaskMerged      TaskStage = "merged"
	TaskQAFailed    TaskStage = "qa_failed"
	TaskDone        TaskStage = "done"
	TaskNeedsPerson TaskStage = "needs_person"
)

type Task struct {
	Kind       StepKind         `json:"kind,omitempty"`
	Background *BackgroundBrief `json:"background,omitempty"`
	Wait       *StepWait        `json:"wait,omitempty"`
	Result     Proof            `json:"result,omitempty"`
	Name       string           `json:"name,omitempty"`
	// Session is the worker action that owns this step's worker session. Core
	// records it with the dispatch reservation, so the live set is derivable
	// before any process exists.
	Session        string    `json:"session,omitempty"`
	PRGeneration   uint64    `json:"pr_generation"`
	Continuations  int       `json:"continuations"`
	ThreadHandle   string    `json:"thread_handle,omitempty"`
	Repairs        string    `json:"repairs,omitempty"`
	RepairedBy     string    `json:"repaired_by,omitempty"`
	ResultRevision string    `json:"result_revision,omitempty"`
	RepairAsked    bool      `json:"repair_asked"`
	ID             string    `json:"id"`
	Done           string    `json:"done"`
	Repository     string    `json:"repository"`
	Base           string    `json:"base"`
	Area           string    `json:"area"`
	Worker         string    `json:"worker"`
	Resources      Resources `json:"resources"`
	DependsOn      []string  `json:"depends_on"`
	Stage          TaskStage `json:"stage"`
	Attempt        int       `json:"attempt"`
	Recoveries     int       `json:"recoveries"`
	EffectsSettled bool      `json:"effects_settled"`
	Conflicts      int       `json:"conflicts"`
	QARepairs      int       `json:"qa_repairs"`
	NodeID         string    `json:"node_id"`
	Head           string    `json:"head"`
	PR             PRState   `json:"pr"`
	Merge          Proof     `json:"merge"`
	QA             Proof     `json:"qa"`
}
type GoalState string

const (
	GoalDefining GoalState = "defining"
	GoalRunning  GoalState = "running"
	GoalWaiting  GoalState = "waiting"
	GoalChecking GoalState = "checking"
	GoalDone     GoalState = "done"
	GoalStopped  GoalState = "stopped"
	GoalBlocked  GoalState = "blocked"
)

type Goal struct {
	StepGraph         *StepGraph          `json:"step_graph,omitempty"`
	Conversation      string              `json:"conversation"`
	Turn              string              `json:"turn"`
	RecordID          string              `json:"record_id"`
	RecordRevision    string              `json:"record_revision"`
	DefinitionPending bool                `json:"definition_pending"`
	DefinitionText    string              `json:"-"`
	AssessedFailure   string              `json:"assessed_failure,omitempty"`
	Scope             GoalScope           `json:"-"`
	Assumptions       []string            `json:"-"`
	Decisions         []GoalDecision      `json:"-"`
	Criteria          []Criterion         `json:"-"`
	Checks            []CriterionEvidence `json:"checks,omitempty"`
	CheckRound        int64               `json:"check_round"`
	State             GoalState           `json:"state"`
	Revision          int64               `json:"revision"`
	Rank              int                 `json:"rank,omitempty"`
	Paused            bool                `json:"paused,omitempty"`
	ManagerTurns      int                 `json:"manager_turns"`
	AttentionID       string              `json:"attention_id,omitempty"`
	BlockedReason     string              `json:"blocked_reason,omitempty"`
	CreatedAt         int64               `json:"created_at"`
	Deadline          int64               `json:"deadline,omitempty"`
	ID                string              `json:"id"`
	Person            string              `json:"person"`
	Agent             string              `json:"agent"`
	Summary           string              `json:"-"`
	Done              string              `json:"-"`
	ServiceTarget     string              `json:"service_target"`
	ServiceRepository string              `json:"service_repository,omitempty"`
	NoServiceReason   string              `json:"no_service_reason"`
	Tasks             []Task              `json:"tasks"`
	TargetRevision    string              `json:"target_revision"`
	MergedRevisions   map[string]string   `json:"merged_revisions,omitempty"`
	Deployment        Proof               `json:"deployment"`
	Verification      Proof               `json:"verification"`
	Waits             []Wait              `json:"waits,omitempty"`
	Evidence          []Proof             `json:"evidence,omitempty"`
}

type ActionKind string

const (
	Assign         ActionKind = "assign"
	Resume         ActionKind = "resume"
	InspectPR      ActionKind = "inspect_pr"
	Merge          ActionKind = "merge"
	RunQA          ActionKind = "qa"
	RepairConflict ActionKind = "repair_conflict"
	RepairQA       ActionKind = "repair_qa"
	Deploy         ActionKind = "deploy"
	Verify         ActionKind = "verify"
	Observe        ActionKind = "observe"
	Decide         ActionKind = "decide"
	PlanGoal       ActionKind = "plan_goal"
)

const CheckCriterion ActionKind = "check_criterion"

const ContinueWorker ActionKind = "continue_worker"

type Action struct {
	StepKind             StepKind        `json:"step_kind,omitempty"`
	RecordRevision       string          `json:"record_revision,omitempty"`
	InputVersion         *InputVersion   `json:"input_version,omitempty"`
	Superseded           bool            `json:"superseded,omitempty"`
	RetirementReconciled bool            `json:"retirement_reconciled,omitempty"`
	ObserveActionID      string          `json:"observe_action_id,omitempty"`
	Signal               string          `json:"signal,omitempty"`
	FailureKey           string          `json:"failure_key,omitempty"`
	CriterionID          string          `json:"criterion_id,omitempty"`
	Check                *CriterionCheck `json:"check,omitempty"`
	GoalRevision         int64           `json:"goal_revision"`
	CheckRound           int64           `json:"check_round"`
	ID                   string          `json:"id"`
	GoalID               string          `json:"goal_id"`
	TaskID               string          `json:"task_id"`
	Kind                 ActionKind      `json:"kind"`
	Attempt              int             `json:"attempt"`
	NodeID               string          `json:"node_id"`
	Revision             string          `json:"revision"`
	Resources            Resources       `json:"resources"`
	State                string          `json:"state"`
	Epoch                uint64          `json:"epoch"`
	Handle               string          `json:"handle"`
	Deadline             int64           `json:"deadline"`
	Sequence             int             `json:"sequence"`
	ObservedAt           int64           `json:"observed_at"`
	ObserverEpoch        uint64          `json:"observer_epoch"`
	Receipt              json.RawMessage `json:"receipt,omitempty"`
}
type State struct {
	Goals   []Goal   `json:"goals"`
	Nodes   []Node   `json:"nodes"`
	Actions []Action `json:"actions"`
}
type Wait struct {
	GoalID string `json:"goal_id"`
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
	Wake   string `json:"wake"`
	Until  int64  `json:"until"`
}
type Plan struct {
	Frontier []FrontierStep
	Actions  []Action
	Waits    []Wait
	Close    []string
	Block    []string
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func validResources(r Resources) bool {
	return finite(r.CPU) && r.CPU > 0 && r.MemoryMB > 0 && r.DiskMB > 0
}
func has(items []string, id string) bool {
	for _, x := range items {
		if x == id {
			return true
		}
	}
	return false
}
func reserves(a Action) bool {
	return a.State != "finished" && workerAction(a.Kind) && stepKind(a.StepKind) != WaitStep
}

// Slots applies both installed resource caps and the live observation. An
// observed active reservation counts against the cap but not twice against
// measured load. Unknown/stale samples and nonfinite resources fail closed.
func Slots(n Node, actions []Action, request Resources, now time.Time) int {
	if !validResources(request) || !n.Connected || n.Yielding || n.ObservedAt <= 0 || n.ObservedAt > now.Unix() || now.Unix()-n.ObservedAt > int64(SafetySweep/time.Second) || !finite(n.CPUCores) || !finite(n.CPULimit) || !finite(n.LoadCPU) || n.CPUCores <= 0 || n.CPULimit <= 0 || n.LoadCPU < 0 || n.AvailableMemoryMB < 0 || n.MemoryLimitMB <= 0 || n.AvailableDiskMB <= 0 || n.DiskLimitMB <= 0 {
		return 0
	}
	var reserved, pending Resources
	for _, a := range actions {
		if a.NodeID != n.ID || !reserves(a) {
			continue
		}
		if !validResources(a.Resources) {
			return 0
		}
		reserved.CPU += a.Resources.CPU
		reserved.MemoryMB += a.Resources.MemoryMB
		reserved.DiskMB += a.Resources.DiskMB
		observed := has(n.ObservedActions, a.ID) || a.ObserveActionID != "" && has(n.ObservedActions, a.ObserveActionID)
		if !observed {
			pending.CPU += a.Resources.CPU
			pending.MemoryMB += a.Resources.MemoryMB
			pending.DiskMB += a.Resources.DiskMB
		}
	}
	cpu := min(n.CPULimit-reserved.CPU, n.CPUCores-n.LoadCPU-pending.CPU)
	mem := min(n.MemoryLimitMB-reserved.MemoryMB, n.AvailableMemoryMB-pending.MemoryMB)
	disk := min(n.DiskLimitMB-reserved.DiskMB, n.AvailableDiskMB-pending.DiskMB)
	if cpu <= 0 || mem <= 0 || disk <= 0 {
		return 0
	}
	return int(min(math.Floor(cpu/request.CPU), float64(mem/request.MemoryMB), float64(disk/request.DiskMB)))
}

func validateGoal(g Goal) error { return validateGoalState(g, false) }

// Only derivation of an already-persisted correction may temporarily retain
// tasks outside the new scope. Admission and manager proposals remain strict.
func validateGoalState(g Goal, pendingCorrection bool) error {
	if !validCriteria(g, pendingCorrection) || g.ID == "" || g.Person == "" || g.Agent == "" || strings.TrimSpace(g.Done) == "" || (g.ServiceTarget == "") == (g.NoServiceReason == "") {
		return ErrInvalid
	}
	if !g.DefinitionPending && !validServiceRepository(g) {
		return ErrInvalid
	}
	tasks := map[string]Task{}
	for _, t := range g.Tasks {
		if !validStepWork(t) {
			return ErrInvalid
		}
		if _, ok := tasks[t.ID]; ok {
			return ErrInvalid
		}
		tasks[t.ID] = t
	}
	color := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		t, ok := tasks[id]
		if !ok || color[id] == 1 {
			return false
		}
		if color[id] == 2 {
			return true
		}
		color[id] = 1
		seen := map[string]bool{}
		for _, dep := range t.DependsOn {
			if seen[dep] || !visit(dep) {
				return false
			}
			seen[dep] = true
		}
		color[id] = 2
		return true
	}
	for id := range tasks {
		if !visit(id) {
			return ErrInvalid
		}
	}
	return nil
}

// goalRank orders ready placement. A goal admitted before ranks existed has no
// rank; it is older than every ranked goal, because a new goal takes the last
// rank, so it sorts first until a person's move gives it one.
func goalRank(g Goal) int {
	if g.Rank < 0 {
		return 0
	}
	return g.Rank
}

func pending(actions []Action, goal, task string) bool {
	for _, a := range actions {
		if a.GoalID == goal && a.TaskID == task && a.State != "finished" {
			return true
		}
	}
	return false
}
func actionID(a Action) string {
	key := []any{a.GoalID, a.TaskID, a.Kind, a.Attempt, a.Revision, a.Sequence, a.CriterionID, a.GoalRevision, a.CheckRound, a.FailureKey}
	if a.ObserveActionID != "" {
		key = append(key, "observe", a.ObserveActionID)
	}
	raw, _ := json.Marshal(key)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// Derive is a deterministic projection of authoritative observations. It never
// calls a model or an adapter. The caller must persist its actions/reservations
// in the same transaction as the source state before dispatching them.
func Derive(state State, now time.Time) (Plan, error) {
	plan := Plan{}
	reservations := append([]Action(nil), state.Actions...)
	goals := append([]Goal(nil), state.Goals...)
	for i := range goals {
		goals[i].TargetRevision = targetRevision(goals[i])
	}
	sort.Slice(goals, func(i, j int) bool { return goals[i].ID < goals[j].ID })
	nodes := append([]Node(nil), state.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	type candidate struct {
		goal  Goal
		task  Task
		kind  ActionKind
		depth int
		rank  int
	}
	var ready []candidate
	type areaLease struct {
		goal string
		task Task
	}
	var occupied []areaLease
	mergeSlots := map[string]bool{}
	for _, g := range goals {
		if err := validateGoalState(g, g.DefinitionPending); err != nil {
			// Invalid canonical corrections are fenced per goal. Keep their
			// outstanding leases below, and let observation settle their effects
			// while unrelated goals continue deriving work.
			if !g.DefinitionPending || g.AssessedFailure != "definition:"+g.RecordRevision {
				return plan, err
			}
		}
		for _, t := range g.Tasks {
			mergePending := false
			for _, a := range state.Actions {
				if a.GoalID == g.ID && a.TaskID == t.ID && a.Kind == Merge && a.State != "finished" {
					mergePending = true
					mergeSlots[t.Repository+"\x00"+t.Base] = true
				}
			}
			if t.Stage != TaskReady && t.Stage != TaskDone && t.Stage != TaskMerged && t.Stage != TaskQAFailed {
				// A stopped task releases its area only after both its worker
				// and any already-dispatched merge have settled.
				if g.State == GoalStopped && t.EffectsSettled && !mergePending {
					continue
				}
				occupied = append(occupied, areaLease{g.ID, t})
			}
		}
	}
	wait := func(g Goal, t Task, reason, wake string) {
		plan.Waits = append(plan.Waits, Wait{g.ID, t.ID, reason, wake, now.Add(SafetySweep).Unix()})
	}
	add := func(g Goal, t Task, kind ActionKind, revision, node string) {
		a := Action{StepKind: t.Kind, GoalID: g.ID, TaskID: t.ID, Kind: kind, Attempt: t.Attempt, Revision: revision, NodeID: node, State: "queued", Deadline: now.Add(SafetySweep).Unix()}
		if workerAction(kind) {
			// A full plan can remove an ID and add it back in a later revision.
			// Its settled worker history still owns the earlier attempt numbers.
			// Undispatched awards and explicit refusals spend no attempt.
			for _, old := range state.Actions {
				if old.GoalID == g.ID && old.TaskID == t.ID && workerAction(old.Kind) && old.Epoch != 0 && len(old.Receipt) != 0 && !settledRefusal(old) {
					a.Attempt = max(a.Attempt, old.Attempt)
				}
			}
			a.Attempt++
			if stepKind(t.Kind) != WaitStep {
				a.Resources = t.Resources
			}
		}
		if kind == ContinueWorker {
			a.Handle = t.ThreadHandle
		}
		a.GoalRevision = g.Revision
		if kind == PlanGoal {
			a.RecordRevision = g.RecordRevision
		}
		a.InputVersion = inputVersion(g, a)
		a.ID = actionID(a)
		plan.Actions = append(plan.Actions, a)
		reservations = append(reservations, a)
	}
	for _, g := range goals {
		if g.State == GoalDone || g.State == GoalStopped {
			continue
		}
		// A paused goal keeps its waits and its in-progress step, but derives no
		// new step and no further input until the person resumes it.
		if g.Paused {
			continue
		}
		if g.DefinitionPending {
			if g.AssessedFailure == "definition:"+g.RecordRevision {
				wait(g, Task{}, "person", "goal.record.changed")
				continue
			}
			settling := false
			for _, a := range state.Actions {
				if a.GoalID == g.ID && a.TaskID != "" && a.Kind != PlanGoal && a.State != "finished" {
					wait(g, Task{ID: a.TaskID}, "unknown_effect", "effect.observed")
					settling = true
				}
			}
			if settling {
				continue
			}
			if !pending(state.Actions, g.ID, "") {
				add(g, Task{}, PlanGoal, g.RecordRevision, "")
			}
			continue
		}
		if len(g.Tasks) == 0 {
			if !pending(state.Actions, g.ID, "") {
				if g.ManagerTurns == 0 {
					add(g, Task{}, PlanGoal, "", "")
				} else {
					plan.Block = append(plan.Block, g.ID)
				}
			}
			continue
		}
		tasks := map[string]Task{}
		for _, t := range g.Tasks {
			tasks[t.ID] = t
		}
		// Share depths across candidates in this goal and snapshot. Traversing
		// every path through shared ancestors would make derivation exponential.
		depths := make(map[string]int, len(tasks))
		var depth func(string) int
		depth = func(id string) int {
			if n, ok := depths[id]; ok {
				return n
			}
			n := 0
			for _, dep := range tasks[id].DependsOn {
				n = max(n, 1+depth(dep))
			}
			depths[id] = n
			return n
		}
		allDone := true
		orderedTasks := append([]Task(nil), g.Tasks...)
		sort.Slice(orderedTasks, func(i, j int) bool { return orderedTasks[i].ID < orderedTasks[j].ID })
		for _, t := range orderedTasks {
			done := taskDone(t)
			allDone = allDone && done
			if pending(state.Actions, g.ID, t.ID) {
				continue
			}
			if done {
				continue
			}
			kind := ActionKind("")
			switch t.Stage {
			case TaskReady:
				blocked := false
				for _, id := range t.DependsOn {
					d := tasks[id]
					if !stepLanded(d) {
						blocked = true
						break
					}
				}
				if blocked {
					wait(g, t, "dependency", "step.landed")
					continue
				}
				kind = Assign
			case TaskBuilding:
				wait(g, t, "worker", "worker.changed")
				continue
			case TaskDelivered:
				if t.PR.Head == t.Head && t.PR.Checks.proves(t.Head) && t.PR.Review.proves(t.Head) && t.PR.Verified.proves(t.Head) && t.PR.OpenP0P1 == 0 && t.PR.Mergeable {
					slot := t.Repository + "\x00" + t.Base
					if mergeSlots[slot] {
						wait(g, t, "merge_slot", "merge.landed")
					} else {
						add(g, t, Merge, t.Head, "")
						mergeSlots[slot] = true
					}
				} else if t.PR.ObservedAt == 0 || now.Unix()-t.PR.ObservedAt >= int64(SafetySweep/time.Second) {
					add(g, t, InspectPR, t.Head, "")
				} else {
					wait(g, t, "ci_review", "pull_request.changed")
				}
				continue
			case TaskConflicted:
				if t.Conflicts >= 1 {
					wait(g, t, "person", "decision.resolved")
					continue
				}
				kind = RepairConflict
			case TaskNoReceipt:
				kind = ContinueWorker
			case TaskWorkerLost:
				if !t.EffectsSettled {
					wait(g, t, "unknown_effect", "effect.observed")
					continue
				}
				if t.Recoveries >= 1 {
					wait(g, t, "person", "decision.resolved")
					continue
				}
				kind = Resume
			case TaskMerged, TaskDone:
				if !t.Merge.proves(t.Merge.Revision) {
					wait(g, t, "merge_evidence", "merge.landed")
					continue
				}
				add(g, t, RunQA, t.Merge.Revision, "")
				continue
			case TaskQAFailed:
				if t.RepairedBy != "" {
					wait(g, t, "repair", "qa.completed")
				} else if t.RepairAsked {
					wait(g, t, "person", "decision.resolved")
				} else {
					add(g, t, PlanGoal, t.Merge.Revision, "")
				}
				continue
			case TaskNeedsPerson:
				wait(g, t, "person", "decision.resolved")
				continue
			default:
				return Plan{}, ErrInvalid
			}
			ready = append(ready, candidate{g, t, kind, depth(t.ID), goalRank(g)})
		}
		if !allDone || pending(state.Actions, g.ID, "") {
			continue
		}
		if g.TargetRevision == "" {
			wait(g, Task{}, "merge_evidence", "merge.landed")
			continue
		}
		if g.ServiceTarget != "" && !g.Deployment.proves(g.TargetRevision) {
			add(g, Task{}, Deploy, g.TargetRevision, "")
			continue
		}
		if !g.Verification.proves(g.TargetRevision) {
			if g.Verification.fails(g.TargetRevision) {
				failure := fmt.Sprintf("verify:%d:%s", g.Revision, g.TargetRevision)
				if g.AssessedFailure == failure {
					plan.Block = append(plan.Block, g.ID)
				} else {
					a := Action{GoalID: g.ID, Kind: PlanGoal, Revision: g.TargetRevision, GoalRevision: g.Revision, RecordRevision: g.RecordRevision, FailureKey: failure, State: "queued", Deadline: now.Add(SafetySweep).Unix()}
					a.InputVersion = inputVersion(g, a)
					a.ID = actionID(a)
					plan.Actions = append(plan.Actions, a)
				}
			} else {
				add(g, Task{}, Verify, g.TargetRevision, "")
			}
			continue
		}
		if g.CheckRound == 0 {
			g.CheckRound = 1
		}
		failure := ""
		for _, c := range goalCriteria(g) {
			for _, proof := range g.Checks {
				if currentEvidence(g, c, proof) && !proof.Passed {
					failure = fmt.Sprintf("%d:%d:%s", g.Revision, g.CheckRound, c.ID)
					break
				}
			}
			if failure != "" {
				break
			}
		}
		if failure != "" {
			if g.AssessedFailure == failure {
				plan.Block = append(plan.Block, g.ID)
			} else {
				a := Action{GoalID: g.ID, Kind: PlanGoal, GoalRevision: g.Revision, RecordRevision: g.RecordRevision, FailureKey: failure, State: "queued", Deadline: now.Add(SafetySweep).Unix()}
				a.InputVersion = inputVersion(g, a)
				a.ID = actionID(a)
				plan.Actions = append(plan.Actions, a)
			}
			continue
		}
		missing := false
		for _, c := range goalCriteria(g) {
			if criterionMet(g, c) {
				continue
			}
			a := Action{GoalID: g.ID, Kind: CheckCriterion, Revision: criterionRevision(g, c), CriterionID: c.ID, Check: &c.Check, GoalRevision: g.Revision, CheckRound: g.CheckRound, State: "queued", Deadline: now.Add(SafetySweep).Unix()}
			a.InputVersion = inputVersion(g, a)
			a.ID = actionID(a)
			plan.Actions = append(plan.Actions, a)
			missing = true
			break
		}
		if !missing {
			plan.Close = append(plan.Close, g.ID)
		}
	}
	deadline := func(g Goal) int64 {
		if g.Deadline == 0 {
			return math.MaxInt64
		}
		return g.Deadline
	}
	sort.SliceStable(ready, func(i, j int) bool {
		a, b := ready[i], ready[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if deadline(a.goal) != deadline(b.goal) {
			return deadline(a.goal) < deadline(b.goal)
		}
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		if a.goal.CreatedAt != b.goal.CreatedAt {
			return a.goal.CreatedAt < b.goal.CreatedAt
		}
		if a.goal.ID != b.goal.ID {
			return a.goal.ID < b.goal.ID
		}
		return a.task.ID < b.task.ID
	})
	for _, c := range ready {
		g, t, kind := c.goal, c.task, c.kind
		if kind == Assign {
			plan.Frontier = append(plan.Frontier, FrontierStep{g.ID, t.ID, stepKind(t.Kind)})
		}
		if stepKind(t.Kind) == WaitStep {
			add(g, t, kind, "", "")
			continue
		}
		overlaps := false
		for _, held := range occupied {
			if held.goal == g.ID && held.task.ID == t.ID {
				continue
			}
			if stepKind(held.task.Kind) != WaitStep && held.task.Area != "" && t.Area != "" && held.task.Repository == t.Repository && areasOverlap(held.task.Area, t.Area) {
				overlaps = true
				break
			}
		}
		if overlaps {
			wait(g, t, "area", "merge.landed")
			continue
		}
		best := ""
		score := -1.0
		personal := true
		available, quota := false, false
		for _, n := range nodes {
			if n.Workers[t.Worker] {
				available = true
				quota = quota || n.QuotaReset[t.Worker] <= now.Unix()
			}
			if !n.Workers[t.Worker] || n.QuotaReset[t.Worker] > now.Unix() {
				continue
			}
			if kind != Assign && t.NodeID != "" && n.ID != t.NodeID {
				continue
			}
			slots := Slots(n, reservations, t.Resources, now)
			rank := float64(slots) * t.Resources.CPU / n.CPUCores
			if slots > 0 && (best == "" || personal && !n.ForPerson || personal == n.ForPerson && rank > score) {
				best, score, personal = n.ID, rank, n.ForPerson
			}
		}
		if best == "" {
			if available && !quota {
				wait(g, t, "worker_quota", "quota.changed")
			} else {
				wait(g, t, "host_capacity", "node.changed")
			}
			continue
		}
		add(g, t, kind, t.Head, best)
		occupied = append(occupied, areaLease{g.ID, t})
	}
	return plan, nil
}
