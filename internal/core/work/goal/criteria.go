// SPDX-License-Identifier: Apache-2.0
package goal

import "strings"

type EvidenceKind string

const (
	ReadEvidence     EvidenceKind = "read"
	RunEvidence      EvidenceKind = "check"
	JudgmentEvidence EvidenceKind = "judgment"
)

type GoalScope struct {
	Repositories []string `json:"repositories"`
	Includes     string   `json:"includes"`
	Excludes     string   `json:"excludes"`
}
type GoalDecision struct {
	Statement string `json:"statement"`
	MessageID string `json:"message_id"`
}
type CriterionCheck struct {
	Kind   EvidenceKind `json:"kind"`
	Method string       `json:"method"`
	Target string       `json:"target"`
	TaskID string       `json:"task_id,omitempty"`
	Rubric string       `json:"rubric,omitempty"`
}
type Criterion struct {
	ID        string         `json:"id"`
	Statement string         `json:"statement"`
	Check     CriterionCheck `json:"check"`
}
type CriterionEvidence struct {
	Criterion    string       `json:"criterion"`
	GoalRevision int64        `json:"goal_revision"`
	Round        int64        `json:"round"`
	Target       string       `json:"target"`
	Revision     string       `json:"revision"`
	Kind         EvidenceKind `json:"kind"`
	Ref          string       `json:"ref"`
	Passed       bool         `json:"passed"`
}

// The code lifecycle adds mandatory checks to the person's outcome criteria.
// The caller cannot weaken these by supplying another check under their ids.
func goalCriteria(g Goal) []Criterion {
	out := []Criterion{}
	for _, c := range g.Criteria {
		if !strings.HasPrefix(c.ID, "code:") {
			out = append(out, c)
		}
	}
	for _, t := range g.Tasks {
		out = append(out, Criterion{ID: "code:merge:" + t.ID, Statement: "Task merged", Check: CriterionCheck{Kind: ReadEvidence, Method: "merge.read", Target: t.Repository, TaskID: t.ID}}, Criterion{ID: "code:qa:" + t.ID, Statement: "QA passed on current merged result", Check: CriterionCheck{Kind: RunEvidence, Method: "qa.read", Target: t.Repository, TaskID: t.ID}})
	}
	if g.ServiceTarget != "" {
		out = append(out, Criterion{ID: "code:deploy", Statement: "Deployment observed at the current revision", Check: CriterionCheck{Kind: ReadEvidence, Method: "deploy.read", Target: g.ServiceTarget}})
	}
	target := g.ServiceTarget
	if target == "" {
		target = g.ID
	}
	out = append(out, Criterion{ID: "code:runtime", Statement: g.Done, Check: CriterionCheck{Kind: RunEvidence, Method: "runtime.probe", Target: target}})
	return out
}
func criterionRevision(g Goal, c Criterion) string {
	if c.Check.TaskID != "" {
		for _, t := range g.Tasks {
			if t.ID == c.Check.TaskID {
				if c.Check.Method == "qa.read" {
					return taskResultRevision(t)
				}
				return t.Merge.Revision
			}
		}
		return ""
	}
	return g.TargetRevision
}
func validCriteria(g Goal, pendingCorrection bool) bool {
	if len(g.Criteria) == 0 || len(g.Scope.Repositories) == 0 || strings.TrimSpace(g.Scope.Includes) == "" {
		return false
	}
	repositories := map[string]bool{}
	for _, repo := range g.Scope.Repositories {
		if strings.TrimSpace(repo) == "" || repositories[repo] {
			return false
		}
		repositories[repo] = true
	}
	for _, t := range g.Tasks {
		if !pendingCorrection && !repositories[t.Repository] {
			return false
		}
	}
	canonical := map[string]Criterion{}
	for _, c := range goalCriteria(g) {
		canonical[c.ID] = c
	}
	seen := map[string]bool{}
	custom := 0
	for _, c := range g.Criteria {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Statement) == "" || seen[c.ID] || strings.TrimSpace(c.Check.Method) == "" || strings.TrimSpace(c.Check.Target) == "" {
			return false
		}
		seen[c.ID] = true
		switch c.Check.Kind {
		case ReadEvidence, RunEvidence:
			if c.Check.Rubric != "" {
				return false
			}
		case JudgmentEvidence:
			if strings.TrimSpace(c.Check.Rubric) == "" {
				return false
			}
		default:
			return false
		}
		if c.Check.TaskID != "" {
			found := false
			for _, t := range g.Tasks {
				found = found || t.ID == c.Check.TaskID
			}
			if !found {
				return false
			}
		}
		if strings.HasPrefix(c.ID, "code:") {
			if canonical[c.ID] != c {
				return false
			}
		} else {
			custom++
		}
	}
	for _, d := range g.Decisions {
		if strings.TrimSpace(d.Statement) == "" || strings.TrimSpace(d.MessageID) == "" {
			return false
		}
	}
	return custom > 0
}
func currentEvidence(g Goal, c Criterion, e CriterionEvidence) bool {
	return e.Criterion == c.ID && e.GoalRevision == g.Revision && e.Round == g.CheckRound && e.Round > 0 && e.Target == c.Check.Target && e.Revision == criterionRevision(g, c) && e.Revision != "" && e.Kind == c.Check.Kind && strings.TrimSpace(e.Ref) != ""
}
func criterionMet(g Goal, c Criterion) bool {
	for _, e := range g.Checks {
		if currentEvidence(g, c, e) {
			return e.Passed
		}
	}
	return false
}

func taskResultRevision(t Task) string {
	if t.ResultRevision != "" {
		return t.ResultRevision
	}
	return t.Merge.Revision
}
func taskDone(t Task) bool {
	return t.Stage == TaskDone && t.Merge.proves(t.Merge.Revision) && t.QA.proves(taskResultRevision(t))
}
func freshTask(t Task) bool {
	return t.PRGeneration == 0 && t.Continuations == 0 && t.ThreadHandle == "" && t.Stage == TaskReady && t.Attempt == 0 && t.Head == "" && t.NodeID == "" && t.Merge == (Proof{}) && t.QA == (Proof{}) && t.PR == (PRState{}) && t.Recoveries == 0 && t.Conflicts == 0 && t.QARepairs == 0 && !t.EffectsSettled && t.RepairedBy == "" && t.ResultRevision == "" && !t.RepairAsked
}
