// SPDX-License-Identifier: Apache-2.0
package work

import "reflect"

// Project replays facts without clocks, database reads or worker calls.
func Project(events []Event) (Card, error) {
	var c Card
	for _, e := range events {
		if err := advance(&c, e); err != nil {
			return Card{}, err
		}
	}
	if c.Task.ID == "" {
		return Card{}, ErrNotFound
	}
	return c, nil
}
func advance(c *Card, e Event) error {
	if e.Kind != "created" && (c.Task.ID == "" || e.TaskID != c.Task.ID) {
		return ErrState
	}
	switch e.Kind {
	case "created":
		if c.Task.ID != "" || e.Task == nil || e.Task.ID != e.TaskID {
			return ErrState
		}
		c.Task = *e.Task
		c.Revision = 1
		c.State = "unknown"
		c.Stages = []Stage{{Name: "worker", Source: "unknown"}, {Name: "review", Source: "unknown"}, {Name: "apply", Source: "unknown"}}
	case "waiting_opened":
		if e.Wait == nil || c.Active || c.Run != nil || c.State == "done" || c.State == "stopped" {
			return ErrState
		}
		if c.Waits == nil {
			c.Waits = map[string]Wait{}
		}
		c.Waits[waitKey(*e.Wait)] = *e.Wait
		c.State = "input_needed"
	case "waiting_decided":
		if e.Wait == nil || c.Waits == nil {
			return ErrState
		}
		if _, ok := c.Waits[waitKey(*e.Wait)]; !ok {
			return ErrState
		}
		c.Waits[waitKey(*e.Wait)] = *e.Wait
		if !hasWait(*c) {
			c.State = "working"
		}
	case "goal_decided":
		if e.Decision == nil {
			return ErrState
		}
		c.Decisions = append(c.Decisions, *e.Decision)
		if e.Decision.Decision.Kind != "default" && c.State != "stopped" {
			c.Revision++
			c.State = "working"
		}
	case "started":
		if hasWait(*c) {
			return ErrState
		}
		if c.Active {
			return ErrActiveRun
		}
		if e.Run == nil || e.Run.TaskID != c.Task.ID {
			return ErrInvalid
		}
		generation := 1
		if c.Run != nil {
			generation = c.Run.Generation + 1
		}
		if e.Run.Generation != generation {
			return ErrState
		}
		c.Run = e.Run
		c.Active = true
		c.State = "working"
		c.Receipt = nil
		c.Materials = nil
		c.ResultHead = ""
		c.Verdict = ""
		c.Applied = false
		c.TurnStatus = ""
		c.Stages = []Stage{{Name: "worker", Source: "unknown"}, {Name: "review", Source: "unknown"}, {Name: "apply", Source: "unknown"}}
	case "receipt":
		if c.Run == nil || c.Run.ID != e.RunID || e.Receipt == nil {
			return ErrState
		}
		if err := validateReceipt(*c, *e.Receipt); err != nil {
			return err
		}
		if c.Receipt != nil {
			if reflect.DeepEqual(c.Receipt, e.Receipt) {
				return nil
			}
			return ErrState
		}
		c.Receipt = e.Receipt
		c.Stages[0] = Stage{Name: "worker", Source: "worker_report", Summary: e.Receipt.Outcome}
		if !c.Active {
			setOutcome(c)
		}
	case "retained":
		if c.Active || c.Receipt == nil || c.Receipt.EventID != e.ReceiptID || c.Receipt.Outcome != "ready_for_review" || c.TurnStatus != "completed" || len(e.Materials) == 0 || len(c.Materials) != 0 {
			return ErrState
		}
		for _, m := range e.Materials {
			if m.ID == "" || m.Revision < 1 || m.Path == "" || len(m.SHA256) != 64 {
				return ErrInvalid
			}
		}
		c.Materials = e.Materials
		c.ResultHead = e.Value

	case "finished":
		if c.Run == nil || c.Run.ID != e.RunID {
			return ErrState
		}
		if e.Value != "completed" && e.Value != "failed" && e.Value != "interrupted" {
			return ErrInvalid
		}
		if c.TurnStatus != "" && c.TurnStatus != e.Value {
			return ErrState
		}
		if c.Receipt != nil && c.Receipt.TurnRef.TurnStatus != e.Value {
			return ErrState
		}
		c.Active = false
		c.TurnStatus = e.Value
		setOutcome(c)
	case "unknown":
		if !c.Active {
			return ErrState
		}
		c.State = "unknown"
	case "judged":
		if c.Active || c.Receipt == nil || c.Receipt.EventID != e.ReceiptID || c.Receipt.Outcome != "ready_for_review" || c.TurnStatus != "completed" || e.Evidence == "" {
			return ErrState
		}
		if e.Value != "verified" && e.Value != "needs_changes" {
			return ErrInvalid
		}
		if c.Verdict != "" && c.Verdict != e.Value {
			return ErrState
		}
		c.Verdict = e.Value
		c.Stages[1] = Stage{Name: "review", Source: "core_review", Summary: e.Value}
		if e.Value == "verified" {
			if c.Task.RequiresApply && !c.Applied {
				c.State = "ready_for_review"
			} else {
				c.State = "done"
			}
		} else {
			c.State = "working"
		}
	case "applied":
		if c.Active || c.Receipt == nil || c.Receipt.EventID != e.ReceiptID || c.Verdict != "verified" || e.Evidence == "" || !c.Task.RequiresApply {
			return ErrState
		}
		c.Applied = true
		c.State = "done"
		c.Stages[2] = Stage{Name: "apply", Source: "core_review", Summary: "applied"}
	default:
		return ErrInvalid
	}
	return nil
}
func setOutcome(c *Card) {
	if c.Receipt != nil {
		switch c.Receipt.Outcome {
		case "ready_for_review":
			c.State = "working"
		case "failed":
			c.State = "failed"
		case "interrupted":
			c.State = "stopped"
		case "blocked_needs_input":
			c.State = "input_needed"
		}
	} else {
		switch c.TurnStatus {
		case "failed":
			c.State = "failed"
		case "interrupted":
			c.State = "stopped"
		default:
			c.State = "unknown"
		}
	}
}
