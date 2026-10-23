// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestPublishedInputRetainsSourceProvenance(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	rec := a2a.Record{Agent: agent, Publication: "published", Task: a2a.Task{ID: "opaque-task", History: []a2a.Message{{MessageID: "message", Role: "ROLE_USER", Parts: []a2a.Part{{Data: []byte(`{"task":"write","input":"data"}`)}}}}}}
	event, err := ledger.New(r.db, nil).Admit(t.Context(), guard.EntryForwarded, publicationInput(rec, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if event.SpeakerKind != guard.Forwarded || event.Origin.Locator == "" || event.PersonID != "" {
		t.Fatalf("external input lost its provenance: %+v", event)
	}
}

func TestPublishedTurnExecutesToolButCannotCompleteWithoutWorker(t *testing.T) {
	root, providerPeer := turnRuntime(t)
	// This focused turn test owns scheduling explicitly; the installed-Core
	// scenario separately exercises the durable publication reactor and worker.
	root.publications.stop()
	<-root.publications.done
	now := time.Now()
	oauth := a2a.OAuth{Issuer: "https://issuer.example", Audience: "a2a", IntrospectionURL: "https://issuer.example/introspect", ClientID: "core", Scopes: []string{"tasks"}}
	authenticator := a2a.Authenticator{OAuth: oauth, Secret: func() (secret.Secret, error) { return secret.New("Y29yZTpmaXh0dXJl"), nil }, Transport: channelTransport(func(*http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"active":true,"iss":"https://issuer.example","aud":"a2a","exp":%d,"sub":"caller","client_id":"client","scope":"tasks","token_type":"Bearer"}`, now.Add(time.Hour).Unix())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	principal, err := authenticator.Authenticate(t.Context(), "Bearer synthetic-access")
	if err != nil {
		t.Fatal(err)
	}
	definition := a2a.Definition{ID: "write", Name: "Task", Description: "Task", Seconds: 60, MaxCalls: 4, Tools: []a2a.Tool{{Name: "write_record", Arguments: json.RawMessage(`{"id":"","revision":"","kind":"memo","title":"Published tool","note":"Committed tool evidence"}`)}}, Work: work.WorkerDefinition{Repository: root.root, Base: "dev", Branch: "task", What: "Write result", Why: "Verify result", Scope: []string{"result.txt"}, NotTouch: []string{"design.md"}, Done: "Committed result", DesignRefs: []string{"design.md#result"}, WriteArea: []string{"result.txt"}}}
	alias := filepath.Join(t.TempDir(), "repository")
	if err = os.Symlink(root.root, alias); err != nil {
		t.Fatal(err)
	}
	definition.Work.Repository = alias
	binding := a2a.Binding{Subject: principal.Subject, Client: principal.Client, Expires: principal.Expires, MaxActive: 2, Definitions: []string{"write"}}
	publication := a2a.Publication{ID: "published", Agent: root.agent, Conversation: root.conversation, Definitions: []a2a.Definition{definition}, Bindings: []a2a.Binding{binding}}
	cfg := a2a.Config{Enabled: true, OAuth: oauth, Publications: []a2a.Publication{publication}}
	root.publications.server.Config = func() (a2a.Config, error) { return cfg, nil }
	rec, err := root.publications.store.Admit(t.Context(), publication, binding, definition, principal, a2a.SendRequest{Message: a2a.Message{MessageID: "message", Role: "ROLE_USER", Parts: []a2a.Part{{Data: json.RawMessage(`{"task":"write","input":"bounded data"}`)}}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	admittedWorker := dispatch.Record{Origin: dispatch.Origin{Turn: "turn:a2a-input:" + rec.Task.ID}, Definition: a2a.WorkDefinition(rec)}
	admittedWorker.Definition.Repository = root.root
	if allowed, err := root.authorizePublishedWorker(t.Context(), admittedWorker); err != nil || !allowed {
		t.Fatal("canonical repository lost its owner authority", allowed, err)
	}
	admittedWorker.Definition.WriteArea = []string{"outside.txt"}
	if allowed, err := root.authorizePublishedWorker(t.Context(), admittedWorker); err != nil || allowed {
		t.Fatal("worker definition enlarged its authority", allowed, err)
	}
	view, err := root.publications.view(t.Context(), rec.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := view.journal().Admit(t.Context(), guard.EntryForwarded, publicationInput(rec, now))
	if err != nil {
		t.Fatal(err)
	}
	row, err := inbox.New(root.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: view.conversation, Source: "a2a", NativeID: rec.Task.ID}, Body: []byte(event.Body.Text())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = root.publications.store.Claim(t.Context(), rec.Task.ID); err != nil {
		t.Fatal(err)
	}
	toolItem, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": "write", "name": "write_record", "arguments": string(definition.Tools[0].Arguments)})
	providerPeer.steps = []turnStep{{events: []provider.Event{{Kind: provider.ItemDone, Item: toolItem}, {Kind: provider.ToolCallReady, Call: &provider.ToolCall{ID: "write", Name: "write_record", Arguments: string(definition.Tools[0].Arguments)}}, {Kind: provider.Terminal}}}, {events: turnText("The task is complete.")}}
	done, err := view.queueReaction(t.Context(), view.conversation, func(ctx context.Context, inputs *loop.Inputs) error { return view.run(ctx, inputs, event, row.ID) })
	if err != nil {
		t.Fatal(err)
	}
	if err = awaitTurn(t, done); err != nil {
		t.Fatal(err)
	}
	if err = root.publications.requireTools(t.Context(), rec); err != nil {
		t.Fatal("real Core tool did not commit", err)
	}
	root.publications.sweep(t.Context())
	current, err := root.publications.store.Read(t.Context(), rec.Task.ID)
	if err != nil || current.Task.Status.State != a2a.Failed || len(current.Task.Artifacts) != 0 {
		t.Fatalf("model prose became completion: %+v %v", current, err)
	}
	admittedWorker.Definition = a2a.WorkDefinition(rec)
	admittedWorker.Definition.Repository = root.root
	if allowed, err := root.authorizePublishedWorker(t.Context(), admittedWorker); err != nil || allowed {
		t.Fatal("terminal task retained worker execution authority", allowed, err)
	}
	if len(providerPeer.requests) != 2 {
		t.Fatalf("provider rounds=%d", len(providerPeer.requests))
	}
	for _, declaration := range providerPeer.requests[0].Tools {
		name := declarationName(declaration)
		if name != "write_record" && name != "dispatch_work" {
			t.Fatalf("unapproved tool offered: %s", name)
		}
	}
}
