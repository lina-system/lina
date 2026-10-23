// SPDX-License-Identifier: Apache-2.0
package results

import (
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/kit/result"
	"testing"
)

func TestMockCannotBypassChecksThroughPresentResult(t *testing.T) {
	s, _, _, _ := setup(t)
	body := result.Mock{Request: "Tasks", Checklist: []result.MockItem{{ID: "tasks", Description: "Tasks", Action: "none"}}, Page: result.AssetRef{ID: "page", Revision: 1}, PNG: result.AssetRef{ID: "png", Revision: 1}, StopReason: "all_passed"}
	for _, id := range []string{"elements", "controls", "tokens", "network", "errors", "layout_390", "layout_1280"} {
		body.Checks = append(body.Checks, result.MockCheck{ID: id, Passed: true})
	}
	raw, _ := json.Marshal(body)
	if _, err := s.Present(t.Context(), scope, result.PresentRequest{Type: result.MockType, Title: "Tasks", Summary: "Tasks mock", Body: raw}); err == nil {
		t.Fatal("model self-certified a mock")
	}
}

func TestMockPublicationIdempotenceAndRevisionOwnership(t *testing.T) {
	s, _, _, _ := setup(t)
	body := result.Mock{Request: "Tasks", Checklist: []result.MockItem{{ID: "tasks", Description: "Tasks", Action: "none"}}, Page: result.AssetRef{ID: "page", Revision: 1}, PNG: result.AssetRef{ID: "png", Revision: 1}, StopReason: "all_passed"}
	for _, id := range []string{"elements", "controls", "tokens", "network", "errors", "layout_390", "layout_1280"} {
		body.Checks = append(body.Checks, result.MockCheck{ID: id, Passed: true})
	}
	raw, _ := json.Marshal(body)
	p := result.PresentRequest{Type: result.MockType, Title: "Mock", Summary: "Checked", Body: raw}
	native := scope
	run := "run-1"
	native.TaskID = &run
	first, err := s.PresentMock(t.Context(), native, p, run)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PresentMock(t.Context(), native, p, run)
	if err != nil || again.ID != first.ID || again.Revision != 1 {
		t.Fatal("publication replay made a revision")
	}
	body.Previous = &result.ResultRef{ID: first.ID, Revision: 1}
	body.Request = "Smaller tasks"
	p.Body, _ = json.Marshal(body)
	p.ID = first.ID
	p.BaseRevision = 1
	run = "run-2"
	second, err := s.PresentMock(t.Context(), native, p, run)
	if err != nil || second.Revision != 2 {
		t.Fatalf("redraw: %+v %v", second, err)
	}
	old, err := s.ReadRevision(t.Context(), scope, first.ID, 1)
	if err != nil || old.Revision != 1 || string(old.Body) == string(p.Body) {
		t.Fatal("prior revision lost")
	}
	outsider := scope
	outsider.PersonID = "other"
	if _, err = s.ReadRevision(t.Context(), outsider, first.ID, 1); err == nil {
		t.Fatal("foreign history read")
	}
	run = "run-3"
	if _, err = s.PresentMock(t.Context(), native, p, run); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale redraw: %v", err)
	}
	ordinary := proposal()
	ordinary.ID = first.ID
	ordinary.BaseRevision = 2
	if _, err = s.Present(t.Context(), scope, ordinary); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("ordinary result replaced checked mock: %v", err)
	}
}
