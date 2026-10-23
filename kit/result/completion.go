// SPDX-License-Identifier: Apache-2.0
package result

import (
	"context"
	"slices"
)

type completionPolicyKey struct{}

// WithCompletionPolicy binds a trusted, prepared policy and observation snapshot
// to an operation. It is not accepted from a tool argument.
func WithCompletionPolicy(ctx context.Context, assess func(CompletionRequest) Completion) context.Context {
	return context.WithValue(ctx, completionPolicyKey{}, assess)
}

func CompletionFor(ctx context.Context, request CompletionRequest) Completion {
	if assess, ok := ctx.Value(completionPolicyKey{}).(func(CompletionRequest) Completion); ok {
		return assess(request)
	}
	return AssessCompletion(request, nil, nil)
}

// EvidenceReference names retained observation data, never a statement in a reply.
// Kind describes what was checked; Core resolves the turn and call in scope.
type EvidenceReference struct {
	Kind   string `json:"kind"`
	TurnID string `json:"turn_id"`
	CallID string `json:"call_id"`
}

type CompletionRequest struct {
	WorkKind string              `json:"work_kind"`
	Evidence []EvidenceReference `json:"evidence,omitempty"`
}

// Completion is Core-authored metadata. State is independent of a worker's
// execution state, a manager verdict and a result's preview/saved status.
type Completion struct {
	ID             string              `json:"id"`
	AgentID        string              `json:"agent_id"`
	WorkKind       string              `json:"work_kind"`
	State          string              `json:"state"`
	Label          string              `json:"label"`
	PolicyRevision string              `json:"policy_revision"`
	Required       []string            `json:"required"`
	Evidence       []EvidenceReference `json:"evidence,omitempty"`
}

func DefaultDoneEvidence() map[string]string {
	return map[string]string{"service": "read_back_running", "interface": "real_view", "data": "live_vs_shown", "message": "sent_copy", "code": "owner_path_run", "document": "source_reopened", "purchase": "receipt", "peer_report": "labelled_claim"}
}

// CompletionSchema is shared by the model-facing completion writers.
func CompletionSchema() map[string]any {
	scalar := map[string]any{"type": "string"}
	return map[string]any{"type": "object", "properties": map[string]any{
		"work_kind": map[string]any{"type": "string", "enum": []string{"service", "interface", "data", "message", "code", "document", "purchase", "peer_report"}},
		"evidence":  map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "object", "properties": map[string]any{"kind": scalar, "turn_id": scalar, "call_id": scalar}, "required": []string{"kind", "turn_id", "call_id"}, "additionalProperties": false}},
	}, "required": []string{"work_kind"}, "additionalProperties": false}
}

// AssessCompletion checks all requirements. Missing policy, unknown work and
// references that cannot be resolved fail closed; the caller records the claim.
func AssessCompletion(request CompletionRequest, required []string, resolves func(EvidenceReference) bool) Completion {
	c := Completion{WorkKind: request.WorkKind, State: "claimed", Label: "not verified", Required: slices.Clone(required)}
	base := DefaultDoneEvidence()[request.WorkKind]
	if base == "" {
		return c
	}
	if !slices.Contains(c.Required, base) {
		c.Required = append(c.Required, base)
	}
	found := map[string]bool{}
	for _, ref := range request.Evidence {
		if ref.TurnID != "" && ref.CallID != "" && slices.Contains(c.Required, ref.Kind) && resolves != nil && resolves(ref) {
			if !slices.Contains(c.Evidence, ref) {
				c.Evidence = append(c.Evidence, ref)
			}
			found[ref.Kind] = true
		}
	}
	for _, kind := range c.Required {
		if !found[kind] {
			return c
		}
	}
	c.State, c.Label = "done", "done"
	if request.WorkKind == "peer_report" {
		c.Label = "agent report"
	}
	return c
}
