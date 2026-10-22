// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"context"
	"encoding/json"
)

type MethodRoute struct {
	Kind  string `json:"kind"`
	Skill string `json:"skill"`
}

// DevelopmentMethods is the dev preset's routing data, also exposed by
// skill_index. It returns a fresh slice so callers cannot alter global routing.
func DevelopmentMethods() []MethodRoute {
	return []MethodRoute{
		{"bug_unknown_cause", "app-debugging"},
		{"slow_heavy", "perf"},
		{"post_merge_qa", "qa"},
		{"delivery_check", "verification-gate"},
		{"stuck_worker", "agent-debug"},
		{"ci_failure_group", "build-failure-triage"},
		{"thin_requirement", "issue-requirements"},
		{"agent_comparison", "agent-evaluation"},
		{"masked_failure", "failure-signal-audit"},
	}
}

// MethodID selects one optional procedure. Explicit references override the
// kind's default; unknown kinds and malformed references fail closed.
func MethodID(kind, id string) (string, error) {
	if kind != "" {
		found := false
		for _, route := range DevelopmentMethods() {
			if route.Kind == kind {
				found = true
				if id == "" {
					id = route.Skill
				}
				break
			}
		}
		if !found {
			return "", ErrInvalid
		}
	}
	if id != "" && !validID(id) {
		return "", ErrInvalid
	}
	return id, nil
}

func (s *Store) ResolveMethod(ctx context.Context, kind, id string) (*Body, error) {
	id, err := MethodID(kind, id)
	if err != nil || id == "" {
		return nil, err
	}
	if s == nil {
		return nil, ErrNotFound
	}
	versions, err := s.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if version.ID == id {
			body, err := s.Read(ctx, version)
			if err != nil {
				return nil, err
			}
			if err := instructionOnly(body.Files); err != nil {
				return nil, err
			}
			return &body, nil
		}
	}
	return nil, ErrNotFound
}

// MethodPacket adds the resolved immutable instructions as data in the initial
// task packet. It neither writes worker files nor changes worker configuration.
func MethodPacket(task string, method *Body) (string, error) {
	if method == nil {
		return task, nil
	}
	raw, err := json.Marshal(struct {
		Task   string `json:"task_packet"`
		Method *Body  `json:"method"`
	}{task, method})
	return string(raw), err
}
