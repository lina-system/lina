// SPDX-License-Identifier: Apache-2.0
package goal

import "testing"

func TestCriterionRejectsUncollectedEvidence(t *testing.T) {
	g := finishedGoal()
	for _, kind := range []EvidenceKind{ReadEvidence, RunEvidence, JudgmentEvidence, "citation", "receipt", "model", "turn", "exit_code"} {
		t.Run(string(kind), func(t *testing.T) {
			c := g.Criteria[0]
			c.Check.Kind = kind
			e := CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: kind, Ref: "claimed-success", Passed: true}
			if currentEvidence(g, c, e) {
				t.Fatal("a reference or completion claim without collected evidence met the criterion")
			}
		})
	}
}

func TestCitationCriterionCanBeAdmitted(t *testing.T) {
	g := oneGoal(readyTask("a"))
	g.Criteria[0].Check.Kind = "citation"
	if err := validateGoal(g); err != nil {
		t.Fatalf("citation checks are required evidence: %v", err)
	}
}

func TestCollectedEvidenceRequiresTheRecordOutputOrJudgmentInputs(t *testing.T) {
	read := EvidenceRead{Target: "document", Revision: "v1", Content: "The source passage."}
	for _, tc := range []struct {
		name      string
		check     CriterionCheck
		collected CollectedEvidence
		valid     bool
	}{
		{"external", CriterionCheck{Kind: ReadEvidence}, CollectedEvidence{Record: []byte(`{"time":"09:00"}`)}, true},
		{"sentence", CriterionCheck{Kind: ReadEvidence}, CollectedEvidence{Record: []byte(`"done"`)}, false},
		{"exit", CriterionCheck{Kind: RunEvidence}, CollectedEvidence{}, false},
		{"output", CriterionCheck{Kind: RunEvidence}, CollectedEvidence{Output: "2 checks passed"}, true},
		{"citations", CriterionCheck{Kind: CitationEvidence}, CollectedEvidence{Reads: []EvidenceRead{read}, Output: "Passage resolves"}, true},
		{"citation-no-input", CriterionCheck{Kind: CitationEvidence}, CollectedEvidence{Output: "Passage resolves"}, false},
		{"judgment", CriterionCheck{Kind: JudgmentEvidence, Rubric: "Answers the question"}, CollectedEvidence{Reads: []EvidenceRead{read}, Rubric: "Answers the question", Judgment: "The passage answers it"}, true},
		{"judgment-no-input", CriterionCheck{Kind: JudgmentEvidence, Rubric: "Answers the question"}, CollectedEvidence{Rubric: "Answers the question", Judgment: "Done"}, false},
		{"different-rubric", CriterionCheck{Kind: JudgmentEvidence, Rubric: "Answers the question"}, CollectedEvidence{Reads: []EvidenceRead{read}, Rubric: "Exists", Judgment: "Yes"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.collected.Valid(tc.check); got != tc.valid {
				t.Fatalf("valid=%v want=%v", got, tc.valid)
			}
		})
	}
}

func TestPluginReadbackMatchesTheRequestedFields(t *testing.T) {
	c := CriterionCheck{Kind: ReadEvidence, Expected: []byte(`{"time":"09:00","place":{"room":"A"},"attendees":["owner"]}`)}
	for _, tc := range []struct {
		record string
		match  bool
	}{
		{`{"id":"event","time":"09:00","place":{"room":"A","floor":2},"attendees":["owner"]}`, true},
		{`{"id":"event","time":"10:00","place":{"room":"A"},"attendees":["owner"]}`, false},
		{`{"time":"09:00","place":{"room":"A"}}`, false},
		{`{"time":"09:00","place":{"room":"A"},"attendees":["someone"]}`, false},
		{`{"receipt":"accepted","passed":true}`, false},
	} {
		if got := (&CollectedEvidence{Record: []byte(tc.record)}).Matches(c); got != tc.match {
			t.Fatalf("record %s: match=%v", tc.record, got)
		}
	}
}
