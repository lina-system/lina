// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/kit/protocol"
	"slices"
)

var GitHubCapability = protocol.Capability{Name: "github", Version: "1.0"}

var GitHubMergeCapability = protocol.Capability{Name: "github_merge", Version: "1.0"}

type GitHubService interface {
	ObserveGitHub(context.Context, Session, protocol.WorkRequest) (protocol.WorkResult, error)
}

func GitHubOperations(service GitHubService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWorkRequest: {Capability: GitHubCapability, AdditionalCapabilities: []protocol.Capability{GitHubMergeCapability}, CapabilityFor: func(e protocol.Envelope) protocol.Capability {
		p, ok := e.Payload.(protocol.WorkRequest)
		if ok && p.Operation != nil && (*p.Operation == "github_read" || *p.Operation == "github_watch") {
			return GitHubCapability
		}
		return GitHubMergeCapability
	}, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.WorkRequest)
		if !ok || !validGitHubRequest(p) {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		if *p.Operation != "github_read" && *p.Operation != "github_watch" && *p.Operation != "github_merge_status" && !s.CanDecide {
			return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
		}
		r, err := service.ObserveGitHub(ctx, s, p)
		effect := protocol.EffectNone
		if *p.Operation == "github_merge" && r.State != nil {
			switch *r.State {
			case "merged":
				if r.Source != nil && *r.Source == "receipt" {
					effect = protocol.EffectApplied
				}
			case "unknown", "queued":
				effect = protocol.EffectUnknown
			}
		}
		return Result{Kind: protocol.KindWorkResult, Payload: r, Effect: effect}, err
	}}}
}

// Semantic field ownership is checked in addition to the closed wire schema.
func validGitHubRequest(p protocol.WorkRequest) bool {
	if p.Operation == nil {
		return false
	}
	allowed := []string{"operation"}
	required := []string{}
	switch *p.Operation {
	case "github_read", "github_watch":
		required = []string{"repository", "pr_number"}
	case "github_bind":
		required = []string{"binding_id", "repository", "pr_number", "base", "clone", "protected_refs", "agent_id"}
	case "github_accept":
		required = []string{"binding_id", "head"}
	case "github_merge", "github_observe_merge", "github_merge_status":
		required = []string{"binding_id"}
	default:
		return false
	}
	allowed = append(allowed, required...)
	raw, _ := json.Marshal(p)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for key := range fields {
		if !slices.Contains(allowed, key) {
			return false
		}
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}
