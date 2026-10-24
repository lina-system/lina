// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/results"
)

// StartLine is the one line said when a goal is handed over: what will be
// done, what done means, the assumptions, and what the person will be asked.
func StartLine(g Goal) (string, error) {
	outcome := oneLine(g.Summary)
	done := oneLine(g.Done)
	if outcome == "" || done == "" {
		return "", ErrInvalid
	}
	tokens := personTokens(g)
	line := fmt.Sprintf("Starting: %s. Done means %s. Assumptions: %s. I will ask: %s.", outcome, done, speakList(g.Assumptions, tokens, "none", "kept with the goal"), speakList(g.Asks, tokens, "nothing yet", "kept with the goal"))
	if err := results.RejectPersonLine(line, tokens); err != nil {
		return "", err
	}
	return line, nil
}

// CloseLine is the one summary when a goal closes. It begins with the outcome
// word and gives one evidence clause per criterion. Stopped and blocked say
// what is missing.
func CloseLine(g Goal) (string, error) {
	var word string
	switch g.State {
	case GoalDone:
		word = "done"
	case GoalStopped:
		word = "stopped"
	case GoalBlocked:
		word = "blocked"
	default:
		return "", ErrConflict
	}
	parts := []string{word + "."}
	if outcome := oneLine(g.Summary); outcome != "" {
		parts = append(parts, outcome+".")
	}
	if g.State == GoalBlocked {
		reason := oneLine(g.BlockedReason)
		if reason == "" {
			reason = "a decision is needed"
		}
		parts = append(parts, reason+".")
	}
	var met, missing []string
	for _, c := range g.Criteria {
		clause := oneLine(c.Statement)
		if clause == "" {
			continue
		}
		if g.State == GoalDone || criterionMet(g, c) {
			met = append(met, clause)
		} else {
			missing = append(missing, clause)
		}
	}
	if len(met) > 0 {
		parts = append(parts, strings.Join(met, "; ")+".")
	}
	if g.State != GoalDone {
		if len(missing) == 0 {
			missing = []string{"the remaining done condition"}
		}
		parts = append(parts, "Missing: "+strings.Join(missing, "; ")+".")
	}
	if yours := joinOr(g.PersonTasks, ""); yours != "" {
		parts = append(parts, "For you: "+yours+".")
	}
	line := strings.Join(parts, " ")
	if err := results.RejectPersonLine(line, personTokens(g)); err != nil {
		return "", err
	}
	return line, nil
}

// RunningRow is one goal task on the running list. A landing changes the
// state and keeps the person-facing name. A technical name is not shown.
func RunningRow(t Task) (summary, state string) {
	summary = oneLine(t.Name)
	if summary == "" || results.RejectPersonLine(summary, nil) != nil {
		summary = "In progress"
	}
	switch t.Stage {
	case TaskNeedsPerson:
		state = "needs_input"
	case TaskDone:
		state = "done"
	default:
		state = "working"
	}
	return summary, state
}

// AcceptProse refuses a goal whose start line or criterion statements would
// be rejected. Admission and correction call it before anything is saved.
func AcceptProse(g Goal) error {
	checked := g
	checked.Criteria = goalCriteria(g)
	if _, err := StartLine(checked); err != nil {
		return err
	}
	for _, c := range checked.Criteria {
		if err := results.RejectPersonLine(c.Statement, personTokens(checked)); err != nil {
			return err
		}
	}
	for _, task := range g.PersonTasks {
		if err := results.RejectPersonLine(task, personTokens(checked)); err != nil {
			return err
		}
	}
	return nil
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// speakList keeps clauses the person can hear. A stored clause that names an
// id, path, or check is omitted and replaced with a plain marker so the
// record can keep it without putting it on the start line.
func speakList(items []string, tokens []string, empty, omitted string) string {
	var kept []string
	dropped := false
	for _, item := range items {
		item = oneLine(item)
		if item == "" {
			continue
		}
		if results.RejectPersonLine(item, tokens) != nil {
			dropped = true
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		if dropped {
			return omitted
		}
		return empty
	}
	if dropped {
		kept = append(kept, omitted)
	}
	return strings.Join(kept, "; ")
}

func joinOr(items []string, fallback string) string {
	parts := []string{}
	for _, item := range items {
		if item = oneLine(item); item != "" {
			parts = append(parts, item)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, "; ")
}

func personTokens(g Goal) []string {
	tokens := []string{}
	add := func(token string) {
		token = strings.TrimSpace(token)
		if token != "" {
			tokens = append(tokens, token)
		}
	}
	// Check methods are check names even when they are ordinary words.
	// Branches, paths and ids that already look technical are named too.
	// A plain task or criterion id is not forbidden: the line never prints it,
	// and forbidding the word would reject the person's own sentence.
	for _, c := range g.Criteria {
		add(c.Check.Method)
		if technicalToken(c.ID) {
			add(c.ID)
		}
		if technicalToken(c.Check.Target) {
			add(c.Check.Target)
		}
	}
	for _, repo := range g.Scope.Repositories {
		if technicalToken(repo) {
			add(repo)
		}
	}
	if technicalToken(g.ServiceTarget) {
		add(g.ServiceTarget)
	}
	for _, t := range g.Tasks {
		add(t.Base)
		if technicalToken(t.Area) {
			add(t.Area)
		}
		if technicalToken(t.Repository) {
			add(t.Repository)
		}
		if technicalToken(t.ID) {
			add(t.ID)
		}
	}
	return tokens
}

func technicalToken(token string) bool {
	return strings.ContainsAny(token, ":/\\._") || personTechnicalToken(token)
}

func personTechnicalToken(token string) bool {
	return results.RejectPersonLine("x "+token+" y", nil) != nil
}
