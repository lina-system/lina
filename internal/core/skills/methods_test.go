// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"testing"

	builtin "github.com/thisisjun786/lina/skills"
)

func TestDevelopmentCatalogAndMethodRouting(t *testing.T) {
	s, _, _ := fixture(t, builtin.Builtins())
	catalog, err := s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"bug_unknown_cause": "app-debugging", "slow_heavy": "perf", "post_merge_qa": "qa",
		"delivery_check": "verification-gate", "stuck_worker": "agent-debug", "ci_failure_group": "build-failure-triage",
		"thin_requirement": "issue-requirements", "agent_comparison": "agent-evaluation", "masked_failure": "failure-signal-audit",
	}
	for kind, id := range expected {
		found := false
		for _, v := range catalog {
			if v.ID == id {
				found = true
			}
		}
		if !found {
			t.Errorf("catalog missing %s", id)
		}
		body, err := s.ResolveMethod(t.Context(), kind, "")
		if err != nil || body == nil || body.Version.ID != id || body.Files["SKILL.md"].Text == "" {
			t.Errorf("route %s: %+v %v", kind, body, err)
		}
	}
	if len(DevelopmentMethods()) != len(expected) {
		t.Fatal("incomplete routes")
	}
	for _, route := range DevelopmentMethods() {
		if expected[route.Kind] != route.Skill {
			t.Errorf("wrong route %+v", route)
		}
	}
	for _, tc := range [][2]string{{"unknown", ""}, {"", "unknown"}, {"", "../meta"}, {"", "qa,perf"}} {
		if _, err := s.ResolveMethod(t.Context(), tc[0], tc[1]); err == nil {
			t.Errorf("accepted %v", tc)
		}
	}
	if body, err := s.ResolveMethod(t.Context(), "", ""); err != nil || body != nil {
		t.Fatalf("optional: %+v %v", body, err)
	}
}

func TestDevelopmentMethodPinsCatalogOverride(t *testing.T) {
	s, _, _ := fixture(t, builtin.Builtins())
	original, err := s.ResolveMethod(t.Context(), "", "qa")
	if err != nil {
		t.Fatal(err)
	}
	text := "---\nname: qa\ndescription: Synthetic override.\n---\nInspect the synthetic boundary.\n"
	if _, err = s.Patch(t.Context(), Patch{ID: "qa", Expected: 0, Files: Bundle{"SKILL.md": {Text: text}}}); err != nil {
		t.Fatal(err)
	}
	next, err := s.ResolveMethod(t.Context(), "post_merge_qa", "")
	if err != nil || next.Files["SKILL.md"].Text != text || next.Version.Commit == original.Version.Commit {
		t.Fatalf("override: %+v %v", next, err)
	}
	pinned, err := s.Read(t.Context(), original.Version)
	if err != nil || pinned.Files["SKILL.md"].Text != original.Files["SKILL.md"].Text {
		t.Fatal("snapshot drifted", err)
	}
}
