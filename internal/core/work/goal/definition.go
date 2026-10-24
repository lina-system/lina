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

// ProjectRecordID is the project record that stores a goal's definition.
// A value that already uses that record id is kept. An empty id does not resolve.
func ProjectRecordID(goalID string) (string, bool) {
	if goalID == "" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(goalID, "goal:"); ok && rest != "" {
		return goalID, true
	}
	return "goal:" + goalID, true
}

// GoalID accepts a goal id or its project record id.
func GoalID(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	if rest, ok := strings.CutPrefix(id, "goal:"); ok && rest != "" {
		return rest, true
	}
	return id, true
}

func (e *Engine) prepareDefinition(ctx context.Context, g *Goal) error {
	if e.cfg.Records == nil {
		return ErrInvalid
	}
	if g.RecordID != "" || g.RecordRevision != "" {
		return ErrInvalid
	}
	// Stable id makes an interrupted record publication recoverable on admission.
	recordID, ok := ProjectRecordID(g.ID)
	if !ok {
		return ErrInvalid
	}
	g.RecordID = recordID
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

type goalSentenceRef struct {
	ID             string `json:"id"`
	Person         string `json:"person"`
	Agent          string `json:"agent"`
	RecordID       string `json:"record_id"`
	RecordRevision string `json:"record_revision"`
}

// GoalSentences returns each goal's canonical sentence, keyed by goal id.
// The execution ledger does not store that prose. A missing or unreadable
// definition omits only that goal. A ledger failure, or any other records
// error, fails the call so a projection does not invent the remaining names.
func (e *Engine) GoalSentences(ctx context.Context) (map[string]string, error) {
	if e.cfg.Records == nil {
		return nil, ErrInvalid
	}
	rows, err := e.journal.Records(ctx, "goal")
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for _, row := range rows {
		// The title is in its definition; decoding tasks and receipts here
		// repeats execution work for every progress refresh.
		var g goalSentenceRef
		if err = json.Unmarshal(row.Body, &g); err != nil {
			return nil, err
		}
		sentence, readErr := e.goalSentence(ctx, g)
		if readErr != nil {
			if errors.Is(readErr, ErrDefinitionMissing) || errors.Is(readErr, ErrInvalid) {
				continue
			}
			return nil, readErr
		}
		if sentence != "" {
			titles[g.ID] = sentence
		}
	}
	return titles, nil
}

func (e *Engine) goalSentence(ctx context.Context, g goalSentenceRef) (string, error) {
	if cached, ok := e.definitions.Load(definitionKey{g.RecordID, g.RecordRevision}); ok {
		saved := cached.(savedDefinition)
		if saved.revision == g.RecordRevision {
			return saved.definition.Outcome, nil
		}
	}
	r, rev, err := e.cfg.Records.Read(ctx, g.RecordID)
	if err != nil {
		return "", err
	}
	d, err := parseDefinition(r, g.Person, g.Agent)
	if err != nil {
		return "", err
	}
	e.definitions.Store(definitionKey{g.RecordID, rev}, savedDefinition{d, rev, string(r.Data)})
	return d.Outcome, nil
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
