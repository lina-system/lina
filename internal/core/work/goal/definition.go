// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrDefinitionMissing = errors.New("goal_definition_missing")
var ErrStaleDefinition = errors.New("goal_stale_revision")

type DefinitionRecord struct {
	ID, Path string
	Data     []byte
}
type DefinitionChange struct {
	ID, Base, Path string
	Data           []byte
}

// Definitions is supplied by Core composition. Work owns execution only; the
// records family owns canonical Markdown, versioning and publication recovery.
type Definitions interface {
	Read(context.Context, string) (DefinitionRecord, string, error)
	Commit(context.Context, DefinitionChange, string, string) (string, error)
}
type Definition struct {
	ServiceRepository string         `json:"service_repository,omitempty"`
	Outcome           string         `json:"outcome"`
	Done              string         `json:"done"`
	Scope             GoalScope      `json:"scope"`
	Assumptions       []string       `json:"assumptions,omitempty"`
	Decisions         []GoalDecision `json:"decisions,omitempty"`
	Criteria          []Criterion    `json:"criteria"`
}

type DefinitionReceipt struct {
	GoalID   string `json:"goal_id"`
	RecordID string `json:"record_id"`
	Revision string `json:"revision"`
}

// Revise changes the canonical definition of an admitted goal. The caller
// supplies its authenticated conversation identity, never model-provided actors.
// Task/evidence state and recorded person decisions are not correction inputs.
func (e *Engine) Revise(ctx context.Context, id, base, person, agent string, d Definition) (DefinitionReceipt, error) {
	var receipt DefinitionReceipt
	if id == "" || base == "" || strings.TrimSpace(d.Outcome) == "" || len(d.Decisions) != 0 {
		return receipt, ErrInvalid
	}
	s, err := e.Snapshot(ctx)
	if err != nil {
		return receipt, err
	}
	for _, g := range s.Goals {
		if g.ID != id {
			continue
		}
		if g.Person != person || g.Agent != agent || g.State == GoalStopped {
			return receipt, ErrInvalid
		}
		r, rev, err := e.cfg.Records.Read(ctx, g.RecordID)
		if err != nil {
			return receipt, err
		}
		if rev != base {
			return receipt, ErrStaleDefinition
		}
		current, err := parseDefinition(r, person, agent)
		if err != nil {
			return receipt, err
		}
		// The accepted record revision may be ahead of the execution ledger.
		// Preserve omitted fields from that canonical basis, not its old cache.
		g.ServiceRepository = serviceRepository(Goal{ServiceTarget: g.ServiceTarget, ServiceRepository: current.ServiceRepository, Scope: current.Scope})
		g.Summary, g.Done, g.Scope = d.Outcome, d.Done, d.Scope
		g.Assumptions, g.Criteria, g.Decisions = d.Assumptions, d.Criteria, current.Decisions
		if d.ServiceRepository != "" {
			g.ServiceRepository = d.ServiceRepository
		}
		if !validServiceRepository(g) {
			return receipt, ErrInvalid
		}
		// Scope corrections retain old task projections until the manager
		// interprets the new definition and their dispatched effects settle.
		g.DefinitionPending = true
		if err := validateGoal(g); err != nil {
			return receipt, err
		}
		rev, err = e.cfg.Records.Commit(ctx, DefinitionChange{ID: r.ID, Base: base, Path: r.Path, Data: definitionBytes(g)}, person, agent)
		if err != nil {
			return receipt, err
		}
		receipt = DefinitionReceipt{GoalID: id, RecordID: r.ID, Revision: rev}
		// Install the existing revision fence and wake the running engine now;
		// restart also reloads a committed correction if this call is interrupted.
		return receipt, e.RefreshDefinitions(ctx)
	}
	return receipt, ErrInvalid
}

// Definitions are immutable per record revision. In-flight journal transactions
// may still reference the prior revision while a refresh publishes a new one.
type definitionKey struct{ id, revision string }

type savedDefinition struct {
	definition     Definition
	revision, text string
}

func definition(g Goal) Definition {
	criteria := []Criterion{}
	for _, c := range g.Criteria {
		if !strings.HasPrefix(c.ID, "code:") {
			criteria = append(criteria, c)
		}
	}
	return Definition{ServiceRepository: g.ServiceRepository, Outcome: g.Summary, Done: g.Done, Scope: g.Scope, Assumptions: g.Assumptions, Decisions: g.Decisions, Criteria: criteria}
}
func applyDefinition(g *Goal, d Definition) {
	g.Summary = d.Outcome
	g.Done = d.Done
	g.Scope = d.Scope
	g.ServiceRepository = d.ServiceRepository
	g.ServiceRepository = serviceRepository(*g)
	g.Assumptions = d.Assumptions
	g.Decisions = d.Decisions
	g.Criteria = d.Criteria
	g.Criteria = goalCriteria(*g)
}
func definitionBytes(g Goal) []byte {
	d := definition(g)
	return []byte(fmt.Sprintf("---\nid: %s\nkind: goal\nstate: active\nperson_id: %s\nagent_id: %s\ngoal: %s\n---\n\n# %s\n\n%s\n", encode(g.RecordID), encode(g.Person), encode(g.Agent), encode(d), g.Summary, g.Done))
}

// ParseDefinition reads a canonical goal record for its owning person and
// agent. Readers that project display data without an engine use it to reach the
// goal's own sentence; execution state never comes from this record.
func ParseDefinition(r DefinitionRecord, person, agent string) (Definition, error) {
	return parseDefinition(r, person, agent)
}
func parseDefinition(r DefinitionRecord, person, agent string) (Definition, error) {
	var d Definition
	front, _, ok := bytes.Cut(r.Data, []byte("\n---\n"))
	if !ok {
		return d, ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for _, line := range strings.Split(string(front), "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if ok {
			if _, duplicate := fields[key]; duplicate {
				return d, ErrInvalid
			}
			fields[key] = json.RawMessage(value)
		}
	}
	var p, a string
	if json.Unmarshal(fields["person_id"], &p) != nil || json.Unmarshal(fields["agent_id"], &a) != nil || p != person || a != agent || json.Unmarshal(fields["goal"], &d) != nil {
		return d, ErrInvalid
	}
	return d, nil
}
func (e *Engine) prepareDefinition(ctx context.Context, g *Goal) error {
	if e.cfg.Records == nil {
		return ErrInvalid
	}
	if g.RecordID != "" || g.RecordRevision != "" {
		return ErrInvalid
	}
	// Stable id makes an interrupted record publication recoverable on admission.
	g.RecordID = "goal:" + g.ID
	r, rev, err := e.cfg.Records.Read(ctx, g.RecordID)
	if errors.Is(err, ErrDefinitionMissing) {
		_, err = e.cfg.Records.Commit(ctx, DefinitionChange{ID: g.RecordID, Path: "projects/" + actionID(Action{GoalID: g.ID}) + ".md", Data: definitionBytes(*g)}, g.Person, g.Agent)
		if err != nil {
			return err
		}
		r, rev, err = e.cfg.Records.Read(ctx, g.RecordID)
	}
	if err != nil {
		return err
	}
	d, err := parseDefinition(r, g.Person, g.Agent)
	if err != nil {
		return err
	}
	// An interrupted legacy admission may have published a single-repository
	// definition without the additive field. Compare its effective binding;
	// leave the saved bytes and revision intact.
	comparable := d
	comparable.ServiceRepository = serviceRepository(Goal{ServiceRepository: d.ServiceRepository, ServiceTarget: g.ServiceTarget, Scope: d.Scope})
	expected := definition(*g)
	expected.ServiceRepository = serviceRepository(*g)
	if !bytes.Equal(encode(comparable), encode(expected)) {
		return ErrConflict
	}
	g.RecordRevision = rev
	e.definitions.Store(definitionKey{g.RecordID, rev}, savedDefinition{d, rev, string(r.Data)})
	return nil
}
func (e *Engine) hydrate(g *Goal) error {
	cached, ok := e.definitions.Load(definitionKey{g.RecordID, g.RecordRevision})
	if !ok {
		return ErrConflict
	}
	saved := cached.(savedDefinition)
	if saved.revision != g.RecordRevision {
		return ErrConflict
	}
	restoreMergedRevisions(g)
	applyDefinition(g, saved.definition)
	g.TargetRevision = targetRevision(*g)
	g.DefinitionText = saved.text
	return nil
}

// RefreshDefinitions reads records outside any canon transaction. A changed
// definition fences execution until one manager turn interprets the correction.
func (e *Engine) RefreshDefinitions(ctx context.Context) error {
	rows, err := e.journal.Records(ctx, "goal")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var g Goal
		if err = json.Unmarshal(row.Body, &g); err != nil {
			return err
		}
		r, rev, err := e.cfg.Records.Read(ctx, g.RecordID)
		if err != nil {
			return err
		}
		d, err := parseDefinition(r, g.Person, g.Agent)
		if err != nil {
			return err
		}
		candidate := g
		candidate.DefinitionPending = true
		applyDefinition(&candidate, d)
		invalid := validateGoalState(candidate, true) != nil
		e.definitions.Store(definitionKey{g.RecordID, rev}, savedDefinition{d, rev, string(r.Data)})
		if rev == g.RecordRevision && (!invalid || g.State == GoalStopped || g.DefinitionPending && g.AssessedFailure == "definition:"+rev) {
			continue
		}
		eventID := "goal.record:" + g.ID + ":" + rev
		if invalid {
			// Also quarantine an invalid revision installed by an older Core.
			// Its original revision event must remain append-only history.
			eventID += ":invalid"
		}
		err = e.journal.Update(ctx, Transition{ID: eventID, Kind: "goal.revised", At: e.cfg.Now().Unix()}, func(v *View) error {
			current, err := loadGoal(v, g.ID)
			if err != nil {
				return err
			}
			if current.RecordRevision == rev && (!invalid || current.DefinitionPending && current.AssessedFailure == "definition:"+rev) {
				return errNoChange
			}
			if current.RecordRevision != g.RecordRevision {
				return ErrConflict
			}
			if current.State == GoalStopped {
				return errNoChange
			}
			restoreMergedRevisions(&current)
			nextRepository := serviceRepository(Goal{ServiceRepository: d.ServiceRepository, ServiceTarget: current.ServiceTarget, Scope: d.Scope})
			if current.ServiceRepository != nextRepository {
				current.Deployment = Proof{}
				current.Verification = Proof{}
			}
			current.ServiceRepository = nextRepository
			current.RecordRevision = rev
			current.Revision++
			current.DefinitionPending = true
			current.State = GoalRunning
			current.AttentionID = ""
			current.BlockedReason = ""
			current.AssessedFailure = ""
			if invalid {
				blockCorrection(&current, "The goal needs a valid done condition and criteria. Correct its record to continue.")
			}
			current.Waits = nil
			current.Checks = nil
			current.CheckRound++
			if err = retireUndispatchedActions(v, &current); err != nil {
				return err
			}
			if err = e.retireRevisedActions(v, &current); err != nil {
				return err
			}
			return put(v, "goal", g.ID, current)
		})
		if err != nil && !errors.Is(err, errNoChange) {
			return err
		}
	}
	return nil
}

// Execution history and evidence stay in the journal. Only the current task
// projection drops superseded work after its effects have settled. A landed
// dependency remains satisfied; an unlanded one requires replanning dependents.
func retainScopedTasks(repositories []string, tasks []Task) []Task {
	removed := map[string]Task{}
	for _, task := range tasks {
		if !has(repositories, task.Repository) {
			removed[task.ID] = task
		}
	}
	for changed := true; changed; {
		changed = false
		for _, task := range tasks {
			if _, ok := removed[task.ID]; ok {
				continue
			}
			for _, id := range task.DependsOn {
				if predecessor, ok := removed[id]; ok && !predecessor.Merge.proves(predecessor.Merge.Revision) {
					removed[task.ID] = task
					changed = true
					break
				}
			}
		}
	}
	kept := []Task{}
	for _, task := range tasks {
		if _, ok := removed[task.ID]; ok {
			continue
		}
		dependencies := []string{}
		for _, id := range task.DependsOn {
			if _, ok := removed[id]; !ok {
				dependencies = append(dependencies, id)
			}
		}
		task.DependsOn = dependencies
		kept = append(kept, task)
	}
	return kept
}
