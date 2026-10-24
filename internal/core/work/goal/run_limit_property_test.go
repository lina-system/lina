// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestRunLimitHoldsAcrossGeneratedPasses derives many passes over generated
// goals, host reports, quota rows and the person's limit changes. The oracle
// never calls Derive, Slots or runLimitFor: it counts each report's steps and
// places the ready steps itself, then checks what the run limit promises from
// the pass after each change.
func TestRunLimitHoldsAcrossGeneratedPasses(t *testing.T) {
	for seed := uint64(0); seed < 80; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runLimitPasses(t, seed) })
	}
}

func runLimitPasses(t *testing.T, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	now := time.Unix(100, 0)
	goals := runLimitGoals(rng)
	nodes := runLimitNodes(rng, now)
	choice := RunLimit{Automatic: true}
	var actions []Action
	for pass := range 24 {
		switch rng.IntN(6) {
		case 0:
			nodes = runLimitNodes(rng, now)
		case 1:
			for i := range nodes {
				nodes[i].LoadCPU = float64(rng.IntN(int(nodes[i].CPUCores) + 1))
			}
		case 2:
			// A blocked quota row parks a worker until its stated reset.
			for i := range nodes {
				if rng.IntN(2) == 0 {
					nodes[i].QuotaReset = map[string]int64{"stub": now.Unix() + 60}
				} else {
					nodes[i].QuotaReset = map[string]int64{"stub": 0}
				}
			}
		case 3:
			if rng.IntN(2) == 0 {
				choice = RunLimit{}
			} else {
				choice = RunLimit{Value: 1 + rng.IntN(3)}
			}
		case 4:
			choice = RunLimit{Automatic: true}
		}
		if !choice.Automatic && choice.Value == 0 {
			choice = RunLimit{Automatic: true}
		}
		for i := range nodes {
			nodes[i].ObservedAt = now.Unix()
		}
		state := State{Goals: append([]Goal(nil), goals...), Nodes: nodes, Actions: append([]Action(nil), actions...), Limits: map[string]RunLimit{"person": choice}}
		before := encode(state)
		plan, err := Derive(state, now)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if !reflect.DeepEqual(before, encode(state)) {
			t.Fatalf("pass %d mutated the ledger", pass)
		}
		wantLimit, wantCause := runLimitOracle(choice, nodes, now)
		limit, ok := plan.RunLimits["person"]
		if !ok || limit.Current != wantLimit {
			t.Fatalf("pass %d: limit = %+v, want %d", pass, limit, wantLimit)
		}
		wantReason := ReasonSlot
		if choice.Automatic {
			wantReason = wantCause
		}
		// The plan only adds work; a step in progress is never retired here.
		for _, a := range plan.Actions {
			if a.State != "queued" {
				t.Fatalf("pass %d: unexpected action state: %+v", pass, a)
			}
		}
		// Place the ready steps without Derive: rank order, the limit first,
		// then what the reports can still carry.
		running := runLimitRunning(actions)
		count := len(running)
		free := freeSteps(nodes, actions, now)
		wantStarted := map[string]bool{}
		wantQueued := map[string]int{}
		place := 0
		for _, g := range rankOrderedGoals(goals) {
			if running[g.ID] || !hasReadyTask(g) {
				continue
			}
			if count >= wantLimit {
				place++
				wantQueued[g.ID] = place
				continue
			}
			if free <= 0 {
				continue
			}
			free--
			count++
			wantStarted[g.ID] = true
		}
		gotStarted := map[string]bool{}
		for _, a := range plan.Actions {
			if WorkerAction(a.Kind) && a.TaskID != "" {
				gotStarted[a.GoalID] = true
			}
		}
		if !reflect.DeepEqual(gotStarted, wantStarted) {
			t.Fatalf("pass %d: started %v, want %v (limit %d, %d running)", pass, gotStarted, wantStarted, wantLimit, len(running))
		}
		// Steps already running continue, so the count can exceed a limit that
		// just fell, but never by more than those running goals.
		if inProgress := len(running) + len(wantStarted); inProgress > max(wantLimit, len(running)) {
			t.Fatalf("pass %d: %d goals in progress, want at most %d", pass, inProgress, max(wantLimit, len(running)))
		}
		// Every queued goal has its place in rank order and its one reason, and
		// waits for that reason.
		if len(plan.Queues) != len(wantQueued) {
			t.Fatalf("pass %d: queues = %+v, want %v", pass, plan.Queues, wantQueued)
		}
		limited := map[string]bool{}
		for _, w := range plan.Waits {
			if w.Reason == "run_limit" {
				limited[w.GoalID] = true
			}
		}
		for _, q := range plan.Queues {
			if q.Place == 0 || wantQueued[q.GoalID] != q.Place {
				t.Fatalf("pass %d: queue = %+v, want %v", pass, plan.Queues, wantQueued)
			}
			if q.Reason != wantReason {
				t.Fatalf("pass %d: queue reason = %q, want %q", pass, q.Reason, wantReason)
			}
			if !limited[q.GoalID] {
				t.Fatalf("pass %d: queued %s has no run limit wait: %+v", pass, q.GoalID, plan.Waits)
			}
		}
		// Apply the pass, then land some of the steps in progress.
		for _, a := range plan.Actions {
			if a.TaskID == "" {
				continue
			}
			a.State = "running"
			actions = append(actions, a)
			for i := range goals {
				if goals[i].ID != a.GoalID {
					continue
				}
				if task := goalTask(&goals[i], a.TaskID); task != nil && task.Stage == TaskReady {
					task.Stage = TaskBuilding
				}
			}
		}
		if rng.IntN(2) == 0 {
			continue
		}
		for i := range actions {
			if actions[i].State != "running" || rng.IntN(3) != 0 {
				continue
			}
			actions[i].State = "finished"
			for j := range goals {
				if goals[j].ID != actions[i].GoalID {
					continue
				}
				if task := goalTask(&goals[j], actions[i].TaskID); task != nil {
					task.Stage = TaskDone
					task.Merge = Proof{Revision: task.ID, Ref: "merge-receipt", Passed: true}
					task.QA = Proof{Revision: task.ID, Ref: "qa-receipt", Passed: true}
				}
			}
		}
	}
}

// runLimitOracle counts each report's steps without calling Slots or
// runLimitFor. Generated steps cost one core and their memory and disk fit, so
// a host carries as many steps as its cores allow after load and its cap.
func runLimitOracle(choice RunLimit, nodes []Node, now time.Time) (int, string) {
	if !choice.Automatic && choice.Value > 0 {
		return choice.Value, ReasonSlot
	}
	total, quotaBlocked := 0, false
	for _, n := range nodes {
		if !n.Connected || n.Yielding || n.ObservedAt <= 0 {
			continue
		}
		carried := int(min(n.CPULimit, n.CPUCores-n.LoadCPU))
		if carried <= 0 {
			continue
		}
		usable := false
		for worker := range n.Workers {
			if n.QuotaReset[worker] <= now.Unix() {
				usable = true
			}
		}
		if !usable {
			quotaBlocked = true
			continue
		}
		total += carried
	}
	reason := ReasonLoad
	if quotaBlocked {
		reason = ReasonQuota
	}
	return max(1, total), reason
}

// freeSteps is what the reports can still place, in the same step units.
func freeSteps(nodes []Node, actions []Action, now time.Time) int {
	free := 0
	for _, n := range nodes {
		if !n.Connected || n.Yielding || n.ObservedAt <= 0 {
			continue
		}
		usable := false
		for worker := range n.Workers {
			usable = usable || n.QuotaReset[worker] <= now.Unix()
		}
		if !usable {
			continue
		}
		reserved := 0.0
		for _, a := range actions {
			if a.NodeID == n.ID && a.State != "finished" && WorkerAction(a.Kind) && stepKind(a.StepKind) != WaitStep {
				reserved += a.Resources.CPU
			}
		}
		free += max(0, int(min(n.CPULimit-reserved, n.CPUCores-n.LoadCPU-reserved)))
	}
	return free
}

// runLimitRunning maps each goal holding a run slot: a committed award counts
// whether or not it was claimed, because the goal's step is on its way.
func runLimitRunning(actions []Action) map[string]bool {
	running := map[string]bool{}
	for _, a := range actions {
		if a.State == "finished" || a.TaskID == "" || !WorkerAction(a.Kind) || stepKind(a.StepKind) == WaitStep {
			continue
		}
		running[a.GoalID] = true
	}
	return running
}

// rankOrderedGoals lists the goals in the order the person's rank gives them.
func rankOrderedGoals(goals []Goal) []Goal {
	out := append([]Goal(nil), goals...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Rank > 0) != (b.Rank > 0) {
			return a.Rank <= 0
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ID < b.ID
	})
	return out
}

// hasReadyTask reports a step whose blockers landed. Generated goals chain
// their steps, so at most one is ready at a time.
func hasReadyTask(g Goal) bool {
	done := map[string]bool{}
	for _, task := range g.Tasks {
		done[task.ID] = taskDone(task)
	}
	for _, task := range g.Tasks {
		if taskDone(task) {
			continue
		}
		blocked := false
		for _, dep := range task.DependsOn {
			blocked = blocked || !done[dep]
		}
		if !blocked {
			return true
		}
	}
	return false
}

func runLimitGoals(rng *rand.Rand) []Goal {
	count := 1 + rng.IntN(5)
	goals := make([]Goal, count)
	for i := range goals {
		g := oneGoal()
		g.ID = fmt.Sprintf("g%d", i)
		g.Rank = i + 1
		g.CreatedAt = 50 + int64(i)
		g.State = GoalRunning
		g.Tasks = nil
		for j := 0; j < 1+rng.IntN(2); j++ {
			task := readyTask(fmt.Sprintf("%s-s%d", g.ID, j))
			task.Area = task.ID
			// One core per step, as Capacity's step unit counts.
			task.Resources = Resources{CPU: 1, MemoryMB: 512, DiskMB: 1024}
			if j > 0 {
				task.DependsOn = []string{g.Tasks[j-1].ID}
			}
			g.Tasks = append(g.Tasks, task)
		}
		goals[i] = g
	}
	if rng.IntN(3) == 0 {
		// A person's move swaps two ranks, keeping them dense and distinct.
		i, j := rng.IntN(count), rng.IntN(count)
		goals[i].Rank, goals[j].Rank = goals[j].Rank, goals[i].Rank
	}
	return goals
}

func runLimitNodes(rng *rand.Rand, now time.Time) []Node {
	nodes := make([]Node, 1+rng.IntN(3))
	for i := range nodes {
		cores := float64(1 + rng.IntN(4))
		nodes[i] = Node{ID: fmt.Sprintf("n%d", i), Connected: true, ObservedAt: now.Unix(), CPUCores: cores, CPULimit: cores, LoadCPU: float64(rng.IntN(int(cores) + 1)),
			AvailableMemoryMB: 1 << 20, MemoryLimitMB: 1 << 20, AvailableDiskMB: 1 << 20, DiskLimitMB: 1 << 20, Workers: map[string]bool{"stub": true}, QuotaReset: map[string]int64{"stub": 0}}
	}
	return nodes
}
