// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func researchTask(id string) Task {
	t := readyTask(id)
	t.Kind = BackgroundStep
	t.Worker = "background"
	t.Base, t.Area = "", ""
	t.Background = &BackgroundBrief{Goal: "Answer the questions", Scope: "named sources", NotTouch: "files", Done: "Each question is answered or marked open", Result: "document", Checks: []BackgroundCheck{{Name: "sources", Kind: "citations_resolve"}}}
	return t
}

func researchNode() Node {
	n := readyNode("n")
	n.Workers["background"] = true
	return n
}

func TestPartialLandingDerivesOneFollowUpScopedToTheGap(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(researchTask("notes"))
	g.Criteria = []Criterion{
		{ID: "questions", Statement: "Each question is answered or marked open", Check: CriterionCheck{Kind: JudgmentEvidence, Method: "research.rubric", Target: "document", Rubric: "answered or marked open with a reason", TaskID: "notes"}},
		{ID: "citations", Statement: "Every cited passage resolves", Check: CriterionCheck{Kind: CitationEvidence, Method: "research.citations", Target: "document", TaskID: "notes"}},
	}
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), researchNode()); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil || len(s.Actions) != 1 || s.Actions[0].Kind != Assign {
		t.Fatalf("first run was not derived: %+v %v", s.Actions, err)
	}
	if _, err = e.Claim(t.Context(), s.Actions[0].ID); err != nil {
		t.Fatal(err)
	}
	partial := Observation{Status: OutcomePartial, EffectsSettled: true, Handle: "run-1", Proof: Proof{Revision: "mat:1", Ref: "mat"}, Partial: &ResearchPartial{RunID: "run-1", Missing: []string{"the fee"}, MaterialID: "mat", MaterialRevision: 1}}
	if err = e.Report(t.Context(), s.Actions[0].ID, 1, partial); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	task := s.Goals[0].Tasks[0]
	if task.Stage == TaskDone || task.ResearchFollowUp == nil || task.ResearchFollowUp.Scope != "the fee" || task.ResearchFollowUp.FollowOf != "run-1" {
		t.Fatalf("partial landing did not derive one follow-up on the gap: %+v", task)
	}
	if task.ResearchFollowUp.NotTouch != "files" || len(task.ResearchFollowUp.Refs) != 1 || task.ResearchFollowUp.Refs[0].ID != "mat" {
		t.Fatalf("follow-up widened the brief or dropped the partial result: %+v", task.ResearchFollowUp)
	}
	var follow []Action
	for _, a := range s.Actions {
		if a.Kind == Assign && a.State != "finished" {
			follow = append(follow, a)
		}
	}
	if len(follow) != 1 || follow[0].Attempt != 2 {
		t.Fatalf("follow-up count: %+v", follow)
	}
	if _, err = e.Claim(t.Context(), follow[0].ID); err != nil {
		t.Fatal(err)
	}
	again := Observation{Status: OutcomePartial, EffectsSettled: true, Handle: "run-2", Proof: Proof{Revision: "mat:2", Ref: "mat"}, Partial: &ResearchPartial{RunID: "run-2", Missing: []string{"the fee"}, Open: []ResearchOpen{{Question: "the fee", Reason: "The sources do not state it."}}, MaterialID: "mat", MaterialRevision: 2}}
	if err = e.Report(t.Context(), follow[0].ID, 1, again); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	task = s.Goals[0].Tasks[0]
	if task.Stage != TaskDone || !task.Result.Passed || len(task.ResearchOpen) != 1 || task.ResearchOpen[0].Reason == "" {
		t.Fatalf("open question was not recorded: %+v", task)
	}
	assigns := 0
	for _, a := range s.Actions {
		if a.Kind == Assign {
			assigns++
		}
	}
	if assigns != 2 {
		t.Fatalf("a second partial started another run: %d", assigns)
	}
	if !QuestionsHandled(task.ResearchGap, nil, task.ResearchOpen) {
		t.Fatal("rubric does not count the open question as handled")
	}
}

func TestUnresolvedCitationHoldsTheDocumentCriterion(t *testing.T) {
	t.Parallel()
	task := researchTask("notes")
	task.Stage = TaskDone
	task.Result = Proof{Revision: "doc:1", Ref: "doc", Passed: true}
	g := oneGoal(task)
	g.Revision = 1
	g.CheckRound = 1
	g.Criteria = []Criterion{
		{ID: "questions", Statement: "Each question is answered or marked open", Check: CriterionCheck{Kind: JudgmentEvidence, Method: "research.rubric", Target: "document", Rubric: "answered or marked open with a reason", TaskID: "notes"}},
		{ID: "citations", Statement: "Every cited passage resolves", Check: CriterionCheck{Kind: CitationEvidence, Method: "research.citations", Target: "document", TaskID: "notes"}},
	}
	g.Verification = Proof{Revision: targetRevision(g), Ref: "steps", Passed: true}
	read := EvidenceRead{Target: "document", Revision: "doc:1", Content: "passage"}
	for _, c := range goalCriteria(g) {
		evidence := CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: c.Check.Kind, Ref: "read:" + c.ID, Passed: true}
		switch c.Check.Kind {
		case CitationEvidence:
			evidence.Passed = false
			evidence.Collected = &CollectedEvidence{Reads: []EvidenceRead{read}, Output: "unresolved citation"}
		case JudgmentEvidence:
			evidence.Collected = &CollectedEvidence{Reads: []EvidenceRead{read}, Rubric: c.Check.Rubric, Judgment: "marked open"}
		case ReadEvidence, RunEvidence:
			evidence.Collected = &CollectedEvidence{Output: "runtime read"}
		default:
			t.Fatalf("unexpected evidence kind %s", c.Check.Kind)
		}
		g.Checks = append(g.Checks, evidence)
	}
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Close) != 0 || len(p.Actions) != 0 {
		t.Fatalf("unresolved citation closed or repaired the goal: %+v", p)
	}
	held := false
	for _, w := range p.Waits {
		if w.Reason == "citation" && w.Wake == "evidence.target_changed" {
			held = true
		}
	}
	if !held {
		t.Fatalf("citation criterion was not held open: %+v", p.Waits)
	}
	for i := range g.Checks {
		if g.Checks[i].Kind == CitationEvidence {
			g.Checks[i].Passed = true
			g.Checks[i].Collected.Output = "citation resolves"
		}
	}
	p, err = Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil || len(p.Close) != 1 {
		t.Fatalf("resolved citation did not close the goal: %+v %v", p, err)
	}
	if !QuestionsHandled([]string{"the fee"}, nil, []ResearchOpen{{Question: "the fee", Reason: "The sources do not state it."}}) {
		t.Fatal("open question did not count as handled")
	}
}
