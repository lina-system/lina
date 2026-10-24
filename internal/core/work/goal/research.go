// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"strings"
)

// ResearchOpen is one question the result marks open, with the reason the
// sources cannot answer it.
type ResearchOpen struct {
	Question string `json:"question"`
	Reason   string `json:"reason"`
}

// ResearchPartial is the usable gap from a background run that landed partial.
type ResearchPartial struct {
	RunID            string         `json:"run_id,omitempty"`
	Missing          []string       `json:"missing,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	Open             []ResearchOpen `json:"open,omitempty"`
	Unresolved       []string       `json:"unresolved,omitempty"`
	MaterialID       string         `json:"material_id,omitempty"`
	MaterialRevision int64          `json:"material_revision,omitempty"`
}

// QuestionsHandled reports whether every gap question is answered or marked
// open with a reason. A question that is no longer missing counts as answered.
func QuestionsHandled(gap, missing []string, open []ResearchOpen) bool {
	still := map[string]bool{}
	for _, q := range missing {
		q = strings.TrimSpace(q)
		if q != "" {
			still[q] = true
		}
	}
	marked := map[string]bool{}
	for _, item := range open {
		q := strings.TrimSpace(item.Question)
		if q != "" && strings.TrimSpace(item.Reason) != "" {
			marked[q] = true
		}
	}
	if len(gap) == 0 {
		return len(still) == 0
	}
	for _, q := range gap {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if still[q] && !marked[q] {
			return false
		}
	}
	return true
}

func applyResearchPartial(g *Goal, t *Task, o Observation) error {
	if t == nil || t.Background == nil || o.Partial == nil {
		return ErrInvalid
	}
	if t.ResearchFollowUp == nil {
		if o.Partial.RunID == "" || o.Partial.MaterialID == "" || o.Partial.MaterialRevision < 1 {
			return ErrInvalid
		}
		if len(o.Partial.Missing) == 0 {
			if len(o.Partial.Unresolved) == 0 {
				t.Stage = TaskNeedsPerson
				t.EffectsSettled = true
				return nil
			}
			return landResearchResult(g, t, o)
		}
		gap := append([]string(nil), o.Partial.Missing...)
		follow := *t.Background
		follow.Scope = strings.Join(gap, "\n")
		follow.Done = "Answer what is missing, or mark it open with the reason the sources cannot answer it."
		follow.FollowOf = o.Partial.RunID
		follow.Refs = []BackgroundRef{{ID: o.Partial.MaterialID, Revision: o.Partial.MaterialRevision}}
		t.ResearchGap = gap
		t.ResearchFollowUp = &follow
		t.Stage = TaskReady
		t.EffectsSettled = true
		t.Result = Proof{}
		return nil
	}
	if len(o.Partial.Missing) > 0 && !openCovers(o.Partial.Missing, o.Partial.Open) {
		t.Stage = TaskNeedsPerson
		t.EffectsSettled = true
		return nil
	}
	return landResearchResult(g, t, o)
}

func landResearchResult(g *Goal, t *Task, o Observation) error {
	proof := o.Proof
	if proof.Revision == "" && o.Partial.MaterialID != "" {
		proof.Revision = fmt.Sprintf("%s:%d", o.Partial.MaterialID, o.Partial.MaterialRevision)
	}
	if proof.Ref == "" {
		proof.Ref = o.Partial.MaterialID
	}
	proof.Passed = true
	if !proof.proves(proof.Revision) {
		return ErrInvalid
	}
	t.Result = proof
	t.ResearchOpen = append([]ResearchOpen(nil), o.Partial.Open...)
	t.Stage = TaskDone
	t.EffectsSettled = true
	g.Evidence = append(g.Evidence, proof)
	return nil
}

func citationUnresolved(g Goal, c Criterion) bool {
	if c.Check.Kind != CitationEvidence {
		return false
	}
	for _, proof := range g.Checks {
		if currentEvidence(g, c, proof) && !proof.Passed {
			return true
		}
	}
	return false
}

func openCovers(missing []string, open []ResearchOpen) bool {
	have := map[string]bool{}
	for _, item := range open {
		if strings.TrimSpace(item.Question) != "" && strings.TrimSpace(item.Reason) != "" {
			have[item.Question] = true
		}
	}
	for _, q := range missing {
		if !have[strings.TrimSpace(q)] {
			return false
		}
	}
	return true
}
