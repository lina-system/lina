// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/materials"
	"github.com/thisisjun786/lina/kit/provider"
)

// Delegated managers have an internal conversation. Their background results
// return through the recorded originating conversation, with its own authority.
func (d *codeGoalDriver) goalRunOrigin(ctx context.Context, g goal.Goal) (runs.Origin, error) {
	o := runs.Origin{Person: g.Person, Agent: g.Agent, Conversation: g.Conversation, Turn: g.Turn, Epoch: d.runtime.epoch}
	err := d.runtime.db.Read(ctx, func(tx *canon.Tx) error {
		a, err := readCoding(ctx, tx, g.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Owner != g.Agent || a.Conversation != g.Conversation || a.Origin.Person != g.Person {
			return goal.ErrNeedsPerson
		}
		o.Agent, o.Conversation, o.Turn = a.Origin.Agent, a.Origin.Conversation, a.Origin.Turn
		return nil
	})
	return o, err
}

func researchPartialObservation(r runs.Record) goal.Observation {
	open := make([]goal.ResearchOpen, 0, len(r.Artifact.Open))
	for _, item := range r.Artifact.Open {
		open = append(open, goal.ResearchOpen{Question: item.Question, Reason: item.Reason})
	}
	var unresolved []string
	for _, c := range r.Citations {
		if !c.Resolved {
			unresolved = append(unresolved, c.Anchor.AssetID)
		}
	}
	return goal.Observation{
		Status: goal.OutcomePartial, Handle: r.ID, EffectsSettled: true, Code: "background_partial",
		Proof: goal.Proof{Revision: fmt.Sprintf("%s:%d", r.Material.ID, r.Material.Revision), Ref: r.Material.ID},
		Partial: &goal.ResearchPartial{
			RunID: r.ID, Missing: runs.PartialGap(r), Reason: r.Artifact.OpenReason, Open: open, Unresolved: unresolved,
			MaterialID: r.Material.ID, MaterialRevision: r.Material.Revision,
		},
	}
}

// A start that never admitted a run has no effect to wait for. Settle the
// refusal so the action's reservation is released and the brief can be
// corrected instead of observing an absent receipt forever.
func settledBackgroundRefusal() goal.Observation {
	return goal.Observation{Status: goal.OutcomeNeedsPerson, EffectsSettled: true, Code: "background_not_started"}
}

func (d *codeGoalDriver) backgroundStep(ctx context.Context, g goal.Goal, t goal.Task, a goal.Action, observe bool) (goal.Observation, error) {
	if d.runtime.background == nil || t.Background == nil {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	s := d.runtime.background.service
	origin, err := d.goalRunOrigin(ctx, g)
	if err != nil {
		return goal.Observation{}, err
	}
	var id string
	if observe {
		r, found, err := s.Started(ctx, origin, a.ID)
		if err != nil {
			return goal.Observation{}, err
		}
		if !found {
			return settledBackgroundRefusal(), nil
		}
		id = r.ID
	} else {
		brief := t.Background
		if t.ResearchFollowUp != nil {
			brief = t.ResearchFollowUp
		}
		raw, err := json.Marshal(brief)
		if err != nil {
			return goal.Observation{}, err
		}
		result := s.StartTool(origin).Execute(ctx, provider.ToolCall{ID: a.ID, Name: runs.StartToolName, Arguments: string(raw)})
		if result.Code != loop.OK {
			// A rejected admission creates no run. The durable receipt decides:
			// without one, no effect can have started.
			r, found, err := s.Started(ctx, origin, a.ID)
			if err != nil {
				return goal.Observation{}, err
			}
			if !found {
				return settledBackgroundRefusal(), nil
			}
			id = r.ID
		} else {
			var receipt runs.Receipt
			if err = json.Unmarshal(result.Output, &receipt); err != nil {
				return goal.Observation{}, err
			}
			id = receipt.ID
		}
	}
	// Release is idempotent; the durable native-call receipt prevents a second run.
	if err := s.Release(ctx, id); err != nil {
		return goal.Observation{}, err
	}
	for {
		changed := s.Changed()
		r, err := s.Get(ctx, id)
		if err != nil {
			return goal.Observation{}, err
		}
		if r.State == runs.Landed {
			if r.Outcome == runs.Partial && r.Material.ID != "" && r.Material.Revision >= 1 {
				unresolved := false
				for _, c := range r.Citations {
					if !c.Resolved {
						unresolved = true
						break
					}
				}
				if len(runs.PartialGap(r)) == 0 && !unresolved {
					return goal.Observation{Status: goal.OutcomeNeedsPerson, Handle: id, EffectsSettled: true, Code: "background_incomplete"}, nil
				}
				return researchPartialObservation(r), nil
			}
			if r.Outcome != runs.Complete || r.Material.ID == "" || r.Material.Revision < 1 {
				return goal.Observation{Status: goal.OutcomeNeedsPerson, Handle: id, EffectsSettled: true, Code: "background_incomplete"}, nil
			}
			return goal.Observation{Status: goal.OutcomeSucceeded, Handle: id, EffectsSettled: true, Proof: goal.Proof{Revision: fmt.Sprintf("%s:%d", r.Material.ID, r.Material.Revision), Ref: r.Material.ID, Passed: true}}, nil
		}
		select {
		case <-ctx.Done():
			return goal.Observation{}, ctx.Err()
		case <-changed:
		}
	}
}

func (d *codeGoalDriver) researchCriterion(ctx context.Context, s goal.State, g goal.Goal, t goal.Task, a goal.Action) (goal.Observation, *goal.CollectedEvidence, error) {
	if d.runtime.background == nil || a.Check == nil || t.ID == "" {
		return goal.Observation{}, nil, goal.ErrNeedsPerson
	}
	origin, err := d.goalRunOrigin(ctx, g)
	if err != nil {
		return goal.Observation{}, nil, err
	}
	var latest runs.Record
	var attempt int
	found := false
	for _, action := range s.Actions {
		if action.GoalID != g.ID || action.TaskID != t.ID || action.Kind != goal.Assign || action.State != "finished" || action.Superseded {
			continue
		}
		id := action.ID
		if action.ObserveActionID != "" {
			id = action.ObserveActionID
		}
		record, ok, err := d.runtime.background.service.Started(ctx, origin, id)
		if err != nil {
			return goal.Observation{}, nil, err
		}
		if !ok || record.State != runs.Landed || action.Attempt < attempt {
			continue
		}
		latest, attempt, found = record, action.Attempt, true
	}
	if !found || latest.Material.ID == "" {
		return goal.Observation{}, nil, goal.ErrNeedsPerson
	}
	doc, err := d.researchDocument(ctx, latest)
	if err != nil {
		return goal.Observation{}, nil, err
	}
	proof := goal.Proof{Revision: a.Revision, Ref: latest.Material.ID, Passed: false}
	read := goal.EvidenceRead{Target: a.Check.Target, Revision: a.Revision, Content: doc.body}
	if strings.TrimSpace(read.Content) == "" {
		read.Content = "result"
	}
	collected := &goal.CollectedEvidence{Reads: []goal.EvidenceRead{read}}
	switch a.Check.Method {
	case "research.citations":
		if a.Check.Kind != goal.CitationEvidence {
			return goal.Observation{}, nil, goal.ErrInvalid
		}
		checked, err := materials.Check(ctx, d.runtime.assets, doc.citations)
		if err != nil {
			return goal.Observation{}, nil, err
		}
		proof.Passed = true
		var lines []string
		for _, c := range checked {
			if !c.Resolved {
				proof.Passed = false
				if c.Anchor.AssetID != "" {
					lines = append(lines, c.Anchor.AssetID)
				}
			}
		}
		if proof.Passed {
			collected.Output = "every cited passage resolves"
		} else if len(lines) == 0 {
			collected.Output = "no cited passage"
		} else {
			collected.Output = "unresolved citation " + strings.Join(lines, ", ")
		}
	case "research.rubric":
		if a.Check.Kind != goal.JudgmentEvidence || strings.TrimSpace(a.Check.Rubric) == "" {
			return goal.Observation{}, nil, goal.ErrInvalid
		}
		proof.Passed = goal.QuestionsHandled(t.ResearchGap, doc.missing, doc.open, doc.body)
		collected.Rubric = a.Check.Rubric
		if proof.Passed {
			collected.Judgment = "each question is answered or marked open with its reason"
		} else {
			collected.Judgment = "the listed questions are not answered or marked open"
		}
		collected.Output = collected.Judgment
	default:
		return goal.Observation{}, nil, goal.ErrInvalid
	}
	return goal.Observation{Status: goal.OutcomeSucceeded, Proof: proof}, collected, nil
}

type researchDoc struct {
	body      string
	missing   []string
	open      []goal.ResearchOpen
	citations []materials.Anchor
}

// researchDocument reads each run's stored material at its current revision.
// A later edit or removal of that material is what the criterion sees.
func (d *codeGoalDriver) researchDocument(ctx context.Context, latest runs.Record) (researchDoc, error) {
	records := []runs.Record{latest}
	id := latest.Brief.FollowOf
	for id != "" && len(records) < 3 {
		prior, err := d.runtime.background.service.Get(ctx, id)
		if err != nil {
			return researchDoc{}, err
		}
		records = append(records, prior)
		id = prior.Brief.FollowOf
	}
	parsed := make([]researchDoc, len(records))
	for i, record := range records {
		artifact, citations, err := d.readStoredMaterial(ctx, record.Material)
		if err != nil {
			return researchDoc{}, err
		}
		open := make([]goal.ResearchOpen, 0, len(artifact.Open))
		for _, item := range artifact.Open {
			open = append(open, goal.ResearchOpen{Question: item.Question, Reason: item.Reason})
		}
		parsed[i] = researchDoc{body: artifact.Body, missing: artifact.Missing, open: open, citations: citations}
	}
	var doc researchDoc
	for i := len(parsed) - 1; i >= 0; i-- {
		text := strings.TrimSpace(parsed[i].body)
		if text == "" {
			continue
		}
		if doc.body != "" {
			doc.body += "\n"
		}
		doc.body += text
	}
	doc.missing = parsed[0].missing
	doc.open = parsed[0].open
	seen := map[string]bool{}
	for _, part := range parsed {
		for _, cite := range part.citations {
			key := fmt.Sprintf("%s:%d:%d:%d:%s", cite.AssetID, cite.Revision, cite.Start, cite.End, cite.Hash)
			if seen[key] {
				continue
			}
			seen[key] = true
			doc.citations = append(doc.citations, cite)
		}
	}
	return doc, nil
}

func (d *codeGoalDriver) readStoredMaterial(ctx context.Context, ref runs.Ref) (runs.Artifact, []materials.Anchor, error) {
	if d.runtime.assets == nil || ref.ID == "" || ref.Revision < 1 {
		return runs.Artifact{}, nil, goal.ErrNeedsPerson
	}
	asset, err := d.runtime.assets.Get(ctx, ref.ID)
	if err != nil {
		return runs.Artifact{}, nil, err
	}
	rev := asset.Revision
	if rev < ref.Revision {
		rev = ref.Revision
	}
	reader, err := d.runtime.assets.OpenRead(ctx, ref.ID, rev)
	if err != nil {
		return runs.Artifact{}, nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return runs.Artifact{}, nil, err
	}
	var saved struct {
		Artifact  runs.Artifact        `json:"Artifact"`
		Citations []materials.Citation `json:"Citations"`
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		return runs.Artifact{}, nil, err
	}
	anchors := append([]materials.Anchor{}, saved.Artifact.Citations...)
	if len(anchors) == 0 {
		for _, cite := range saved.Citations {
			anchors = append(anchors, cite.Anchor)
		}
	}
	return saved.Artifact, anchors, nil
}

func researchOpen(r runs.Record) []goal.ResearchOpen {
	out := make([]goal.ResearchOpen, 0, len(r.Artifact.Open))
	for _, item := range r.Artifact.Open {
		out = append(out, goal.ResearchOpen{Question: item.Question, Reason: item.Reason})
	}
	return out
}

// A wait holds no worker lease. Signals trigger an owner-scoped source read;
// dates and expiry use a timer, never a polling loop or a model call.
func (d *codeGoalDriver) waitStep(ctx context.Context, g goal.Goal, t goal.Task, a goal.Action) (goal.Observation, error) {
	w := t.Wait
	if w == nil {
		return goal.Observation{}, goal.ErrInvalid
	}
	deadline := w.At
	if w.RecordID != "" {
		deadline = w.Deadline
	}
	timer := time.NewTimer(max(0, time.Until(time.Unix(deadline, 0))))
	defer timer.Stop()
	for {
		changed := d.runtime.db.Changed()
		if w.RecordID != "" {
			row, err := d.runtime.records.ViewRecord(ctx, g.Person, w.RecordID)
			if err != nil {
				return goal.Observation{}, err
			}
			if row.Revision != w.Revision {
				return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: row.Revision, Ref: w.RecordID, Passed: true}}, nil
			}
		}
		if time.Now().Unix() >= deadline {
			if w.RecordID != "" {
				return goal.Observation{Status: goal.OutcomeNeedsPerson, EffectsSettled: true, Code: "wait_expired"}, nil
			}
			return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: fmt.Sprint(w.At), Ref: "date:" + a.ID, Passed: true}}, nil
		}
		select {
		case <-ctx.Done():
			return goal.Observation{}, ctx.Err()
		case <-timer.C:
		case <-changed:
		}
	}
}

// Verification reads the result's owner and revision again. It does not restart
// a run or reopen a date wait, and an expired or changed result cannot close a goal.
func (d *codeGoalDriver) readStepResult(ctx context.Context, s goal.State, g goal.Goal, t goal.Task) (goal.Proof, error) {
	if t.Stage != goal.TaskDone || !t.Result.Passed || t.Result.Ref == "" || t.Result.Revision == "" {
		return goal.Proof{}, goal.ErrNeedsPerson
	}
	switch t.Kind {
	case goal.BackgroundStep:
		if d.runtime.background == nil {
			return goal.Proof{}, goal.ErrNeedsPerson
		}
		origin, err := d.goalRunOrigin(ctx, g)
		if err != nil {
			return goal.Proof{}, err
		}
		for _, a := range s.Actions {
			if a.GoalID != g.ID || a.TaskID != t.ID || a.Kind != goal.Assign || a.Superseded || a.State != "finished" {
				continue
			}
			id := a.ID
			if a.ObserveActionID != "" {
				id = a.ObserveActionID
			}
			r, found, err := d.runtime.background.service.Started(ctx, origin, id)
			if err != nil {
				return goal.Proof{}, err
			}
			if found && r.State == runs.Landed && backgroundResultAccepted(t, r) && r.Material.ID == t.Result.Ref && fmt.Sprintf("%s:%d", r.Material.ID, r.Material.Revision) == t.Result.Revision {
				return t.Result, nil
			}
		}
	case goal.WaitStep:
		if t.Wait == nil {
			break
		}
		if t.Wait.At > 0 && time.Now().Unix() >= t.Wait.At && fmt.Sprint(t.Wait.At) == t.Result.Revision {
			return t.Result, nil
		}
		if t.Wait.RecordID != "" {
			row, err := d.runtime.records.ViewRecord(ctx, g.Person, t.Wait.RecordID)
			if err != nil {
				return goal.Proof{}, err
			}
			if row.Revision == t.Result.Revision && row.Revision != t.Wait.Revision {
				return t.Result, nil
			}
		}
	case goal.TaskStep, "":
	}
	return goal.Proof{}, goal.ErrNeedsPerson
}

func backgroundResultAccepted(t goal.Task, r runs.Record) bool {
	switch r.Outcome {
	case runs.Complete:
		return true
	case runs.Partial:
		return goal.QuestionsHandled(t.ResearchGap, r.Artifact.Missing, researchOpen(r), r.Artifact.Body)
	case runs.Failed, runs.Cancelled, runs.InputNeeded:
		return false
	}
	return false
}
