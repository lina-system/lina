// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	responseconfig "github.com/thisisjun786/lina/kit/provider/responses/config"
)

type turnStep struct {
	events []provider.Event
	before func(provider.Request)
	err    error
}

// The script replaces only the provider boundary. Preparation, tool dispatch,
// ledger commits and publication use the installed Core composition.
type turnProvider struct {
	steps    []turnStep
	requests []provider.Request
	streams  []*turnStream
}

var _ provider.Adapter = (*turnProvider)(nil)

func (*turnProvider) Provider() provider.ProviderID { return provider.ChatGPT }
func (*turnProvider) Class() provider.Class         { return provider.ModelAPI }
func (*turnProvider) Models(context.Context, lease.Lease, provider.Credential) (provider.ModelList, error) {
	return provider.ModelList{}, fmt.Errorf("unexpected model discovery")
}
func (*turnProvider) Quota(context.Context, provider.Credential) ([]provider.QuotaRow, error) {
	return nil, fmt.Errorf("unexpected quota request")
}
func (p *turnProvider) Stream(ctx context.Context, _ lease.Lease, request provider.Request) (provider.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i := len(p.requests)
	p.requests = append(p.requests, request)
	if i >= len(p.steps) {
		return nil, fmt.Errorf("unexpected provider request %d", i+1)
	}
	step := p.steps[i]
	if step.before != nil {
		step.before(request)
	}
	if step.err != nil {
		return nil, step.err
	}
	s := &turnStream{events: step.events}
	p.streams = append(p.streams, s)
	return s, nil
}

type turnStream struct {
	events []provider.Event
	closed bool
}

func (s *turnStream) Next(ctx context.Context) (provider.Event, error) {
	if err := ctx.Err(); err != nil {
		return provider.Event{}, err
	}
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*turnStream) Meta() provider.Meta { return provider.Meta{Class: provider.ModelAPI} }
func (s *turnStream) Close() error      { s.closed = true; return nil }

func turnRuntime(t *testing.T) (*conversationRuntime, *turnProvider) {
	t.Helper()
	// Make worker discovery independent of the developer's installed CLIs.
	// These sentinels must never be executed by a conversation unit test.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err = os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "gh"} {
		if err = os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	r := controlRuntime(t)
	p := &turnProvider{}
	r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{
			"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1/v1",
			"LINA_RESPONSES_MODEL":    "turn-fixture",
			"LINA_RESPONSES_API_KEY":  "synthetic-turn-key",
		}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	r.route.Adapter = p
	r.identity.TopicCapabilities = map[string]protocol.Capability{}
	if err := r.startRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.startWorkerDispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.background == nil || r.workerDispatch == nil {
		t.Fatal("turn services were not wired")
	}
	installTurnManifest(t, r.root)
	return r, p
}

func installTurnManifest(t *testing.T, root string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, f)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real build gate against this test binary. The remaining
	// provenance describes a synthetic install, never a distributable build.
	m := manifest.Manifest{
		Version: "0.0.1-alpha.dev", Commit: strings.Repeat("a", 40), Date: "2026-10-23",
		Target: runtime.GOOS + "/" + runtime.GOARCH, GoToolchain: runtime.Version(), KitVersion: 1,
		SupportedCombinations: []manifest.Combination{},
		Providers:             map[string]responseconfig.Config{"chatgpt": {Adapter: "responses", RequestFields: provider.FieldList{"model", "input", "store", "stream"}}},
		SQLite: manifest.SQLite{Module: "github.com/ncruces/go-sqlite3", Version: "v0.30.5",
			ModuleSum: "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=", SQLiteVersion: "3.51.2", WasmSHA256: strings.Repeat("d", 64), Execution: "wazero"},
		Binaries: []manifest.Binary{
			{Name: "lina", Size: size, SHA256: fmt.Sprintf("%x", digest.Sum(nil))},
			{Name: "lina-node", Size: size, SHA256: fmt.Sprintf("%x", digest.Sum(nil))},
		},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "system/bin/manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// Admission is durable but execution is explicit, so tests can inspect the
// error returned by run and place steering at a precise provider boundary.
func admitTurnInput(t *testing.T, r *conversationRuntime, id string) (ledger.Event, int64) {
	t.Helper()
	a := &conversationAdmission{db: r.db, person: r.person, agent: r.agent,
		input: inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "client", NativeID: id}, Body: []byte(id)}}
	now := time.Now()
	event, err := ledger.New(a, r.materials).Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: id, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent,
		Surface: ledger.Surface{Kind: "tui", Language: "en"}, Locator: id,
		OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, speaker := event.Body.Text(), protocol.ConversationSpeakerPerson
	if err := r.publish(t.Context(), protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker}); err != nil {
		t.Fatal(err)
	}
	return event, a.receipt.ID
}

func steerTurn(t *testing.T, r *conversationRuntime, event ledger.Event, inboxID int64) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.submitInput(event, inboxID, true, r); err != nil {
		t.Fatal(err)
	}
}

func awaitTurn(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("conversation lane did not finish")
		return nil
	}
}

func turnText(text string) []provider.Event {
	raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	return []provider.Event{{Kind: provider.Delta, Delta: text}, {Kind: provider.ItemDone, Item: raw}, {Kind: provider.Terminal}}
}

func assertTurnRecord(t *testing.T, r *conversationRuntime, input ledger.Event, inboxID int64, state turn.State, rounds int) turn.Record {
	t.Helper()
	record, err := turn.New(r.db).Read(t.Context(), "turn:"+input.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != state || !record.Prepared || record.InboxID != inboxID || record.Conversation != r.conversation {
		t.Fatalf("durable turn: %+v", record)
	}
	ack, err := r.journal().Get(t.Context(), "prepare:"+record.ID)
	if err != nil || ack.ScaffoldKind != "prepare_ack" || ack.ParentID != input.ID || ack.AgentID != r.agent {
		t.Fatalf("prepare acknowledgement: %+v, %v", ack, err)
	}
	for round := range rounds {
		e, err := r.journal().Get(t.Context(), fmt.Sprintf("request:%s:%d", record.ID, round))
		if err != nil || e.ScaffoldKind != "request_assembly" || e.ParentID != ack.ID {
			t.Fatalf("request evidence %d: %+v, %v", round, e, err)
		}
	}
	return record
}

func assertTurnPublished(t *testing.T, r *conversationRuntime, keys ...string) {
	t.Helper()
	start, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var got []string
	for _, item := range start.Snapshot.Items {
		if item.Kind != protocol.KindItemDone {
			continue
		}
		got = append(got, item.Key)
		e, err := r.journal().Get(t.Context(), item.Key)
		if err != nil {
			t.Fatal(err)
		}
		var published protocol.ItemDone
		if err := json.Unmarshal(item.Data, &published); err != nil {
			t.Fatal(err)
		}
		if published.Text == nil || *published.Text != e.Body.Text() || published.Speaker == nil || string(*published.Speaker) != string(e.SpeakerKind) {
			t.Fatalf("published item %s does not match durable content and speaker: %+v", item.Key, published)
		}
	}
	if !reflect.DeepEqual(got, keys) {
		t.Fatalf("published keys = %v, want %v", got, keys)
	}
}

func assertTurnRequests(t *testing.T, p *turnProvider, count int) {
	t.Helper()
	if len(p.requests) != count {
		t.Fatalf("provider requests = %d, want %d", len(p.requests), count)
	}
	for _, s := range p.streams {
		if !s.closed {
			t.Error("provider stream was not closed")
		}
	}
	for _, request := range p.requests {
		if request.Model != "turn-fixture" || request.Purpose != provider.Turn {
			t.Fatalf("request lost model policy: model=%s purpose=%s", request.Model, request.Purpose)
		}
		var names []string
		for _, raw := range request.Tools {
			var declaration struct{ Name string }
			if err := json.Unmarshal(raw, &declaration); err != nil {
				t.Fatal(err)
			}
			names = append(names, declaration.Name)
		}
		// Pin the public names and their assembly order, not schema descriptions
		// or persona prose. This catches omitted families and duplicated tools.
		want := []string{
			"change_persona", "undo_persona", "fill_persona_setting", "fill_work_method",
			"record_emotion", "set_emotion_expression", "record_relationship",
			"ask_question", "resolve_question", "remember_name",
			"skill_notices", "skill_maintenance", "skill_index", "read_skill", "write_skill", "undo_skill",
			"query_records", "read_record", "write_record", "promote_memo", "undo_record", "propose_candidate", "list_candidates",
			"present_result", "read_result", "calculate", "observe_health", "repair_records_index",
			"plan_index", "read_plan", "draft_plan", "start_run", "dispatch_work",
			"prepare_work", "retain_work_result", "report_work_result", "land_work_result",
			"worker_start", "worker_input", "worker_interrupt", "worker_quota", "worker_read_file", "worker_snapshot",
			"create_timer", "timer_status", "cancel_timer",
			"send_handoff", "handoff_status", "begin_artifact_change", "finish_artifact_change",
		}
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("declared tool order = %v, want %v", names, want)
		}
	}
}
