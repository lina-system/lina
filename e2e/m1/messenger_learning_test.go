// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	readcanon "github.com/thisisjun786/lina/internal/moirai/canon"
)

// TestM1MessengerLearningInstalledCore proves the owner's learning path inside
// a running Core: a fake adapter admits the person's records, a pre-watermark
// record makes no model call, a later burst does, and the worker commits a
// common memory item with no source agent.
func TestM1MessengerLearningInstalledCore(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"canon", "system/bin", "system/core"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), provenance, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"messenger-learning"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.root, "run"), 0700); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Input) != 2 {
			emitResponse(w, "Understood.", true)
			return
		}
		var input struct{ Content string }
		var batch memory.MessengerBatch
		if json.Unmarshal(request.Input[1], &input) != nil || json.Unmarshal([]byte(input.Content), &batch) != nil || len(batch.Utterances) == 0 {
			if json.Unmarshal(request.Input[1], &input) != nil || json.Unmarshal([]byte(input.Content), &batch) != nil {
				t.Error("invalid extraction input")
			}
			emitResponse(w, "Understood.", true)
			return
		}
		calls.Add(1)
		for _, utterance := range batch.Utterances {
			if len(utterance.Events) == 0 {
				t.Error("utterance without person evidence")
			}
		}
		items := []memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea in the morning",
			Subject: memory.Subject{Kind: memory.Person, ID: batch.Person}, Basis: memory.Stated,
			Evidence: batch.Utterances[0].Events}}
		output, _ := json.Marshal(items)
		emitResponse(w, string(output), true)
	}))
	defer server.Close()

	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: "person:messenger-learning", Platform: "fixture", Account: "account-1",
		Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	db, closeStore := installedAcceptanceStore(t, h)
	if err := messenger.Open(db, nil, nil).Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := messenger.Open(db, nil, nil).UpsertChats(h.ctx, source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	closeStore()

	gate := make(chan struct{})
	adapter := &learningAdapter{pages: map[string][]messenger.Page{
		"": {{Messages: []messenger.Message{
			message("history", messenger.RoutePerson, start.Add(-time.Hour), "I lived in Busan."),
		}, Next: "c1"}},
		"c1": {{Messages: []messenger.Message{
			message("other", messenger.RouteParticipant, start.Add(time.Minute), "The person lives on Mars."),
			message("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
			message("mine-2", messenger.RoutePerson, start.Add(3*time.Minute), "Especially in the morning."),
		}, Next: "c2"}},
	}, gates: map[messenger.Cursor]chan struct{}{"c1": gate}}

	route, err := modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{
			"LINA_RESPONSES_BASE_URL": server.URL,
			"LINA_RESPONSES_MODEL":    "messenger-fixture",
			"LINA_RESPONSES_API_KEY":  "synthetic-messenger-key",
		}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		done <- boot.Run(runCtx, boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route,
			Messenger: &boot.MessengerConfig{Adapters: map[string]messenger.Adapter{"fixture": adapter}, Interval: 20 * time.Millisecond}})
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("messenger learning Core stopped with %v", err)
				}
			case <-time.After(operationTimeout):
				t.Error("messenger learning Core shutdown timed out")
			}
		})
	}
	defer stop()

	// The first page commits, then the second waits behind the gate. Learning
	// has advanced over the pre-watermark record without a model call.
	adapter.waitFor(t, "c1")
	waitLearningCursor(t, h, source, 1)
	if calls.Load() != 0 {
		t.Fatalf("pre-watermark record reached the model: %d", calls.Load())
	}
	close(gate)
	adapter.waitFor(t, "c2")

	worker := buildCommand(t, "moirai-worker")
	waitJobReady(t, h, "messenger-extraction")
	runWorkerOnce := func() {
		t.Helper()
		p := h.command("--canon", filepath.Join(h.root, "canon/canon.sqlite"), "--socket", filepath.Join(h.root, "run/jobs.sock"), "--once")
		p.command.Path, p.command.Args[0] = worker, worker
		p.start(t)
		p.wait(t, 0)
	}
	runWorkerOnce()
	if calls.Load() != 1 {
		t.Fatalf("learning calls=%d, want 1", calls.Load())
	}
	stop()

	withFileCanon(t, h, func(db *canon.Store) {
		var count int
		var holder, content string
		var record []byte
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM memory_item").Scan(&count); err != nil {
				return err
			}
			return tx.QueryRowContext(h.ctx, "SELECT holder,content,record FROM memory_item").Scan(&holder, &content, &record)
		}); err != nil {
			t.Fatal(err)
		}
		if count != 1 || holder != "all" || content != "Prefers jasmine tea in the morning" {
			t.Fatalf("learned item: count=%d holder=%q content=%q", count, holder, content)
		}
		var item memory.Item
		if err := json.Unmarshal(record, &item); err != nil {
			t.Fatal(err)
		}
		if item.SourceAgent != "" || len(item.Evidence) != 2 {
			t.Fatalf("item shape: %+v", item)
		}
		for _, evidence := range item.Evidence {
			if evidence.SourceAgent != "" || evidence.ID != "messenger:fixture:account-1:dm-1:mine#1" && evidence.ID != "messenger:fixture:account-1:dm-1:mine-2#1" {
				t.Fatalf("evidence: %+v", evidence)
			}
		}
		hits, err := memory.New(db).Recall(h.ctx, memory.RecallScope{Agent: "agent-b", Person: source.Person}, memory.Query{At: time.Now().Unix(), Text: "jasmine"})
		if err != nil || len(hits) != 1 {
			t.Fatalf("recall from another agent: %+v %v", hits, err)
		}
	})
}

// learningAdapter serves scripted pages and can hold one cursor until the test
// releases it.
type learningAdapter struct {
	mu        sync.Mutex
	pages     map[string][]messenger.Page
	gates     map[messenger.Cursor]chan struct{}
	requested []messenger.Cursor
}

func (a *learningAdapter) SignIn(context.Context) (messenger.SignInState, error) {
	return messenger.SignedIn, nil
}
func (a *learningAdapter) ListChats(context.Context) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}, nil
}
func (a *learningAdapter) Page(ctx context.Context, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	a.mu.Lock()
	a.requested = append(a.requested, cursor)
	queue := a.pages[string(cursor)]
	gate := a.gates[cursor]
	var next *messenger.Page
	if len(queue) > 0 {
		page := queue[0]
		next = &page
		a.pages[string(cursor)] = queue[1:]
	}
	a.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return messenger.Page{}, ctx.Err()
		}
	}
	if next != nil {
		return *next, nil
	}
	return messenger.Page{Next: cursor}, nil
}
func (*learningAdapter) Members(context.Context, string) ([]messenger.Author, error) { return nil, nil }
func (*learningAdapter) Live(context.Context, func(messenger.Event) error) error     { return nil }
func (*learningAdapter) FetchAttachment(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (*learningAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Edits: true, Deletions: true}
}

func (a *learningAdapter) waitFor(t *testing.T, cursor messenger.Cursor) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		for _, requested := range a.requested {
			if requested == cursor {
				a.mu.Unlock()
				return
			}
		}
		a.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("adapter was not asked for cursor %q", cursor)
}

// waitLearningCursor observes the committed source cursor. It proves progress
// was written before the second page was allowed through.
func waitLearningCursor(t *testing.T, h *harness, source messenger.Source, want int64) {
	t.Helper()
	absolute, err := filepath.Abs(filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := driver.Open("file:"+filepath.ToSlash(absolute)+"?mode=ro", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA query_only=ON")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		var through int64
		if err := db.QueryRowContext(h.ctx, "SELECT through FROM messenger_learning_cursor WHERE platform=? AND account=?", source.Platform, source.Account).Scan(&through); err == nil && through >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("learning cursor did not advance over the pre-watermark record")
}

func waitJobReady(t *testing.T, h *harness, id string) {
	t.Helper()
	reader, err := readcanon.Open(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		work, err := reader.Ready(h.ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range work {
			if item.Job.ID == id {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s task was not scheduled", id)
}
