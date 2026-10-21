// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/kit/materials"
)

// land holds the service lock. The stable material id makes an interrupted
// material/record handover retryable without creating duplicate artifacts.
func (s *Service) land(ctx context.Context, r *Record, outcome Outcome) error {
	// Commit the result/checkpoint before any material handover. A later Release
	// retries this landing only, retaining cancellation and avoiding model replay.
	if r.PendingOutcome == Cancelled {
		outcome = Cancelled
	}
	r.PendingOutcome = outcome
	if err := s.save(ctx, *r, false); err != nil {
		return err
	}
	citations, err := materials.Check(ctx, s.cfg.Assets, r.Artifact.Citations)
	if err != nil {
		return err
	}
	r.Citations = citations
	resolved := true
	for _, c := range citations {
		if !c.Resolved {
			resolved = false
		}
	}
	r.Checks = nil
	for _, c := range r.Brief.Checks {
		checked := CheckResult{Check: c}
		switch c.Kind {
		case CitationsResolve:
			checked.Passed = resolved
			checked.Verified = true
		case ToolCalled:
			checked.Passed = slices.Contains(r.Calls, c.Name)
			checked.Verified = true
		case Claimed:
			checked.Passed = r.Artifact.Done
		}
		r.Checks = append(r.Checks, checked)
		if !checked.Passed && outcome == Complete {
			outcome = Partial
		}
	}
	if !resolved && outcome != Cancelled {
		outcome = Partial
	}
	if len(r.Artifact.Missing) > 0 && outcome == Complete {
		outcome = Partial
	}
	if r.Artifact.Body != "" || len(r.Checkpoint) > 0 {
		body, err := json.Marshal(struct {
			Type       string
			Artifact   Artifact
			Citations  []materials.Citation
			Checks     []CheckResult
			Checkpoint json.RawMessage
		}{Type: r.Brief.Result, Artifact: r.Artifact, Citations: r.Citations, Checks: r.Checks, Checkpoint: checkpointBytes(r.Checkpoint)})
		if err != nil {
			return err
		}
		body, err = s.masker.json(body)
		if err != nil {
			return err
		}
		agent := r.Origin.Agent
		if agent == "" {
			agent = "core-job"
		}
		a, err := s.cfg.Assets.SaveRunResult(ctx, r.ID, body, assets.Actor{PersonID: r.Origin.Person, AgentID: agent})
		if err != nil {
			return err
		}
		r.Material = Ref{ID: a.ID, Revision: a.Revision}
	}
	r.NoticeVersion++
	r.NoticeAt = s.cfg.Now().UnixNano()
	r.PendingOutcome = ""
	r.Noticed = false
	r.State = Landed
	r.Outcome = outcome
	r.Released = false
	return s.save(ctx, *r, false)
}
func checkpointBytes(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
