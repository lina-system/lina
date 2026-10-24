// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestLocalTaskMasksRewriteBeforeTheMainModel(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	text := "rewrite password=synthetic-secret please"
	masked, err := secrets.Mask(text)
	if err != nil {
		t.Fatal(err)
	}
	projected := masked.DerivedText()
	if strings.Contains(projected, "synthetic-secret") || projected == text {
		t.Fatalf("projection did not mask the rewrite: %q", projected)
	}
	adapter := &turnProvider{steps: []turnStep{{events: []provider.Event{
		{Kind: provider.Delta, Delta: "rewritten"},
		{Kind: provider.Terminal},
	}}}}
	r := &conversationRuntime{
		person:     "person",
		db:         db,
		activities: lifecycle.NewRegistry(nil),
		route:      &modelpath.ResponsesConfig{Adapter: adapter, Model: "main-model"},
	}
	preference := "main"
	result, err := r.localTask(t.Context(), clientsrv.Session{PersonID: "person"}, protocol.Envelope{Payload: protocol.LocalTask{Task: "rewrite", Text: text, Preference: &preference}})
	if err != nil || len(adapter.requests) != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(adapter.requests))
	}
	raw, err := json.Marshal(adapter.requests[0].Items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), projected) || strings.Contains(string(raw), "synthetic-secret") {
		t.Fatalf("main model saw %s, want %s", raw, projected)
	}
	payload, ok := result.Payload.(protocol.LocalTaskResult)
	if !ok || payload.Route != "main" || payload.Model != "main-model" {
		t.Fatalf("result: %+v", result.Payload)
	}
}
