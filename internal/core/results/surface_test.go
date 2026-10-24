// SPDX-License-Identifier: Apache-2.0

package results

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/result"
)

func TestSurfaceBatchPublishesOnlyCompleteRevisions(t *testing.T) {
	t.Parallel()
	store, db, _, _ := setup(t)
	raw, err := os.ReadFile("../../../kit/result/testdata/progress.json")
	if err != nil {
		t.Fatal(err)
	}
	var surface result.Surface
	_ = json.Unmarshal(raw, &surface)
	components, _ := json.Marshal(surface.Components)
	p := result.PresentRequest{Type: result.SurfaceType, Title: "Work", Summary: "Three tasks.", Body: json.RawMessage(`{"messages":[{"version":"v0.9","createSurface":{"surfaceId":"work","catalogId":"lina.cards"}},{"version":"v0.9","updateComponents":{"surfaceId":"work","components":` + string(components) + `}}]}`)}
	card, err := store.PresentSurface(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if card.Revision != 1 || count(t, db, "result_heads") != 1 || count(t, db, "conv_event") != 1 {
		t.Fatal("batch leaked intermediate revisions")
	}
	p.ID = card.ID
	p.BaseRevision = 1
	p.Body = json.RawMessage(`{"messages":[{"version":"v0.9","updateDataModel":{"surfaceId":"work","path":"/temporary","value":"private"}},{"version":"v0.9","updateComponents":{"surfaceId":"work","components":[{"id":"bad","component":"Unknown"}]}}]}`)
	if _, err := store.PresentSurface(t.Context(), scope, p); !errors.Is(err, result.ErrSurfaceValidation) {
		t.Fatalf("invalid batch: %v", err)
	}
	current, err := store.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 1 || count(t, db, "conv_event") != 1 {
		t.Fatal("failed batch changed durable state")
	}
	p.Body = json.RawMessage(`{"messages":[{"version":"v0.9","deleteSurface":{"surfaceId":"work"}}]}`)
	next, err := store.PresentSurface(t.Context(), scope, p)
	if err != nil || next.Revision != 2 {
		t.Fatalf("delete: %+v %v", next, err)
	}
	if _, err := store.PresentSurface(t.Context(), scope, p); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale update: %v", err)
	}
	wrong := scope
	wrong.AgentID = "other"
	if _, err := store.Read(t.Context(), wrong, result.ReadRequest{ID: card.ID}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cross-agent read")
	}
}

func TestSurfaceSaveHasNoMaterialDestination(t *testing.T) {
	t.Parallel()
	if _, err := destination(result.SurfaceType, result.Destination{Kind: "resource"}); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("surface save=%v", err)
	}
}
