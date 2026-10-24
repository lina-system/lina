// SPDX-License-Identifier: Apache-2.0
//go:build storebench_core

package storebench

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/moirai"
	moiraicanon "github.com/thisisjun786/lina/internal/moirai/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// Opt in under dv heavy. The normal Core owns startup, migrations, recovery,
// socket and memory handlers; there is no benchmark-only production endpoint.
func TestHeadlessCore(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-sb-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	r, err := measure(t.Context(), root, generate(4))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a durable queued fill, including material-backed events. Only
	// the running Core may drain it; no search request or test drain does so.
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: 2, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM recall_doc; DELETE FROM recall_pending; INSERT INTO recall_pending(kind,source_id) SELECT kind,source_id FROM recall_source")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = controls.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"storebench"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var in struct {
			Model string
			Input []string
		}
		if json.NewDecoder(request.Body).Decode(&in) != nil || len(in.Input) > embedding.BatchSize {
			t.Error("invalid embedding batch")
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		rows := []map[string]any{}
		for i := range in.Input {
			rows = append(rows, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"model": in.Model, "data": rows})
	}))
	defer endpoint.Close()
	config, _ := json.Marshal(embedding.Config{Endpoint: endpoint.URL, Model: "fixture", Dimension: 2})
	if err = os.WriteFile(filepath.Join(root, "system/embedding.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	ready, done := make(chan struct{}), make(chan struct{})
	var runErr error
	socket := filepath.Join(root, "core.sock")
	go func() {
		runErr = boot.Run(ctx, boot.Config{StateRoot: root, Socket: socket, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.Ready {
				close(ready)
			}
			return nil
		}})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if runErr != nil {
				t.Errorf("Core shutdown: %v", runErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("Core shutdown deadline")
		}
	})
	select {
	case <-ready:
	case <-done:
		t.Fatalf("Core startup: %v", runErr)
	case <-ctx.Done():
		t.Fatal("Core readiness deadline")
	}
	observer, err := driver.Open((&url.URL{Scheme: "file", Path: filepath.Join(root, "canon/canon.sqlite"), RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { observer.Close() })
	reader, err := moiraicanon.Open(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, workerCancel := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- (moirai.Worker{Reader: reader, Core: jobapi.NewClient(filepath.Join(root, "run/jobs.sock"))}).Run(workerCtx, 10*time.Millisecond)
	}()
	t.Cleanup(func() {
		workerCancel()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("worker shutdown deadline")
		}
		reader.Close()
	})
	waitVectors := func() {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var pending, ready int
			if err := observer.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM recall_embed_pending),ready FROM search_index_config WHERE name='recall'").Scan(&pending, &ready); err != nil {
				t.Fatal(err)
			}
			if pending == 0 && ready == 1 {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("embedding queue pending=%d ready=%d", pending, ready)
			case <-ticker.C:
			}
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pending, missing int
		err = observer.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM recall_pending),(SELECT count(*) FROM recall_source s WHERE NOT EXISTS(SELECT 1 FROM recall_doc d WHERE d.kind=s.kind AND d.source_id=s.source_id AND d.revision=s.revision))`).Scan(&pending, &missing)
		if err != nil {
			t.Fatal(err)
		}
		if pending == 0 && missing == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Core recall fill: pending=%d missing=%d", pending, missing)
		case <-ticker.C:
		}
	}
	var documents int
	if err = observer.QueryRowContext(ctx, "SELECT count(*) FROM recall_doc").Scan(&documents); err != nil || documents == 0 {
		t.Fatalf("Core recall documents=%d: %v", documents, err)
	}
	t.Logf("running Core drained queued sources: %d documents", documents)
	waitVectors()
	var vectorCount int
	if err = observer.QueryRowContext(ctx, "SELECT count(*) FROM recall_vec").Scan(&vectorCount); err != nil || vectorCount != documents {
		t.Fatalf("Core vectors=%d documents=%d err=%v", vectorCount, documents, err)
	}
	t.Logf("Moirai filled %d vectors through Core in %d embedding calls", vectorCount, calls.Load())
	client, err := clientapi.DialWithCapabilities(ctx, socket, "0.0.0", []protocol.Capability{{Name: "memory", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if client.Welcome.PersonID != person || len(client.Welcome.Agents) == 0 {
		t.Fatal("Core identity missing")
	}
	a := client.Welcome.Agents[0].AgentID
	request := func(operation string, id *string, text ...string) protocol.MemoryResult {
		t.Helper()
		var query *string
		if len(text) > 0 {
			query = &text[0]
		}
		requestID, err := client.Memory(ctx, protocol.MemoryRequest{Operation: &operation, AgentID: &a, ItemID: id, Text: query})
		if err != nil {
			t.Fatal(err)
		}
		for {
			envelope, err := client.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if envelope.ReplyTo == nil || *envelope.ReplyTo != requestID {
				continue
			}
			if envelope.Error != nil {
				t.Fatalf("memory request refused: %s", envelope.Error.Code)
			}
			result, ok := envelope.Payload.(protocol.MemoryResult)
			if !ok {
				t.Fatal("missing memory result")
			}
			return result
		}
	}
	listed := request("list", nil)
	if listed.Items == nil || len(*listed.Items) != r.MemoryItems-r.ForgetHidden {
		t.Fatal("Core did not recover the benchmark's visible memory")
	}
	semantic := request("list", nil, "growing plants without lexical overlap")
	if semantic.Items == nil || len(*semantic.Items) != 1 {
		t.Fatal("live Core did not use semantic recall")
	}
	id := (*listed.Items)[0].ItemID
	request("forget", &id)
	listed = request("list", nil)
	if listed.Items == nil || len(*listed.Items) != 0 {
		t.Fatal("Core forget did not hide the remaining benchmark item")
	}
	if err = observer.QueryRowContext(ctx, "SELECT count(*) FROM recall_doc WHERE kind='memory'").Scan(&documents); err != nil || documents != 0 {
		t.Fatalf("Core forget left %d recall memory documents: %v", documents, err)
	}
	if err = observer.QueryRowContext(ctx, "SELECT count(*) FROM recall_vec v JOIN recall_doc d ON d.id=v.rowid WHERE d.kind='memory'").Scan(&vectorCount); err != nil || vectorCount != 0 {
		t.Fatal("forget left vectors")
	}
	operation, text := "remember", "도서관에서 공부를 합니다."
	requestID, err := client.Memory(ctx, protocol.MemoryRequest{Operation: &operation, AgentID: &a, Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	for {
		envelope, err := client.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.ReplyTo == nil || *envelope.ReplyTo != requestID {
			continue
		}
		if envelope.Error != nil {
			t.Fatal(envelope.Error.Code)
		}
		break
	}
	if err = observer.QueryRowContext(ctx, "SELECT count(*) FROM recall_doc WHERE kind IN ('event','memory') AND id IN (SELECT rowid FROM recall_text WHERE recall_text MATCH '도서관')").Scan(&documents); err != nil || documents != 2 {
		t.Fatalf("live Core remember indexed %d documents: %v", documents, err)
	}
	waitVectors()
	if err = observer.QueryRowContext(ctx, "SELECT count(*) FROM recall_vec v JOIN recall_doc d ON d.id=v.rowid WHERE d.kind='memory'").Scan(&vectorCount); err != nil || vectorCount != 1 {
		t.Fatal("new memory was not embedded")
	}
	t.Log("live socket recall used vectors; remember queued vectors; forget removed vectors atomically")
}
