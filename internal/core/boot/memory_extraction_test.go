// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestMemoryExtractionJobRegisteredForLiveAgent(t *testing.T) {
	r, _ := turnRuntime(t)
	r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{r.agent: r}}
	r.memoryExtraction = &memoryExtractionRuntime{services: map[string]*jobs.Service{}}
	id := "memory-extraction:" + r.agent
	service, err := (growthJobAPI{r}).service(t.Context(), id)
	if err != nil {
		t.Fatalf("live agent extraction service: %v", err)
	}
	policy, err := service.Policy(t.Context(), id)
	if err != nil || !policy.Enabled || policy.Deterministic {
		t.Fatalf("extraction policy: %+v %v", policy, err)
	}
	if _, err := r.router.view(t.Context(), clientsrv.Session{PersonID: r.person}, r.agent); err != nil {
		t.Fatal(err)
	}
}

func extractionFixture(t *testing.T) (*conversationRuntime, *turnProvider, *jobs.Service) {
	t.Helper()
	r, p := turnRuntime(t)
	r.memoryExtraction = &memoryExtractionRuntime{services: map[string]*jobs.Service{}}
	s, err := r.extractionService(t.Context(), r.agent)
	if err != nil {
		t.Fatal(err)
	}
	return r, p, s
}

func extractionEvent(t *testing.T, r *conversationRuntime, id string, entry guard.Entry, text string) {
	t.Helper()
	person := ""
	if entry == guard.EntryOwner {
		person = r.person
	}
	now := time.Unix(1800000000, 0)
	_, err := r.journal().Admit(t.Context(), entry, ledger.Input{ID: id, AgentID: r.agent, PersonID: person, ConversationID: r.conversation, Surface: ledger.Surface{Kind: "tui"}, Body: text, OccurredAt: now, RecordedAt: now, TimeQuality: "observed"})
	if err != nil {
		t.Fatal(err)
	}
}

func extractionDue(t *testing.T, r *conversationRuntime, s *jobs.Service) jobapi.AuxRequest {
	t.Helper()
	id := extractionPrefix + r.agent
	j, err := s.Policy(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	j.NextRun = time.Time{}
	raw, _ := json.Marshal(j)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE moirai_jobs SET record=? WHERE id=?", raw, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return jobapi.AuxRequest{Epoch: r.epoch, JobID: id, From: j.Watermark, Through: j.Watermark + 1}
}

func extractionStep(t *testing.T, r *conversationRuntime, s *jobs.Service) {
	t.Helper()
	req := extractionDue(t, r, s)
	result, err := s.Aux(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	proposal := jobapi.Proposal{Epoch: req.Epoch, JobID: req.JobID, From: req.From, Through: req.Through, Output: result.Output}
	if _, err := s.Apply(t.Context(), proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), proposal); err != nil {
		t.Fatalf("receipt replay: %v", err)
	}
}

func TestMemoryExtractionSourcesProgressAndRestart(t *testing.T) {
	r, p, s := extractionFixture(t)
	// An entire ineligible prefix advances without any task or model call.
	for i := range 35 {
		extractionEvent(t, r, fmt.Sprint("system-", i), guard.EntrySystem, "Not an owner statement.")
	}
	for range 2 {
		if err := r.queueExtraction(t.Context(), r.agent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Aux(t.Context(), extractionDue(t, r, s)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("empty input: %v", err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,agent_id,person_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body) SELECT 'imported',conversation_id,agent_id,?,'person',surface,occurred_at,recorded_at,time_quality,'{"kind":"import"}','Historical preference.' FROM conv_event WHERE id='system-0'`, r.person)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	extractionEvent(t, r, "first", guard.EntryOwner, "I prefer jasmine tea.")
	extractionEvent(t, r, "second", guard.EntryOwner, "Especially in the morning.")
	extractionEvent(t, r, "assistant", guard.EntryAgent, "The person lives on Mars.")
	output, _ := json.Marshal([]memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea in the morning", Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "first", Precision: memory.Exact}, {Kind: memory.Event, ID: "second", Precision: memory.Exact}}}})
	p.steps = []turnStep{{events: turnText(string(output)), before: func(req provider.Request) {
		if req.Purpose != provider.Auxiliary || len(req.Tools) != 0 || req.Instructions != "" {
			t.Error("wrong auxiliary route")
		}
		var input struct{ Content string }
		var batch memory.ExtractionBatch
		if len(req.Items) != 2 || json.Unmarshal(req.Items[1], &input) != nil || json.Unmarshal([]byte(input.Content), &batch) != nil {
			t.Fatal("missing batch")
		}
		if len(batch.Utterances) != 1 || len(batch.Utterances[0].Events) != 2 {
			t.Fatalf("scope or burst: %+v", batch)
		}
	}}}
	for range 2 {
		if err := r.queueExtraction(t.Context(), r.agent); err != nil {
			t.Fatal(err)
		}
	}
	extractionStep(t, r, s)
	hits, err := memory.New(r.db).Recall(t.Context(), memory.RecallScope{Agent: r.agent, Person: r.person}, memory.Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 1 || hits[0].Basis != memory.Stated || len(hits[0].Evidence) != 2 || hits[0].Evidence[0].Quote != "I prefer jasmine tea." {
		t.Fatalf("memory: %+v %v", hits, err)
	}
	before, err := s.ExtractionCursor(t.Context(), extractionPrefix+r.agent)
	if err != nil {
		t.Fatal(err)
	}
	r.memoryExtraction = &memoryExtractionRuntime{services: map[string]*jobs.Service{}}
	s, err = r.extractionService(t.Context(), r.agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.queueExtraction(t.Context(), r.agent); err != nil {
		t.Fatal(err)
	}
	after, err := s.ExtractionCursor(t.Context(), extractionPrefix+r.agent)
	if err != nil || before != after || len(p.requests) != 1 {
		t.Fatalf("restart cursor/calls: %d %d %d %v", before, after, len(p.requests), err)
	}
	if _, err := s.Aux(t.Context(), extractionDue(t, r, s)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("restart scheduled old evidence: %v", err)
	}
}

func TestMemoryExtractionObsoleteBatchAndFailedCallRetry(t *testing.T) {
	for _, mode := range []string{"revoked", "failed", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			r, p, s := extractionFixture(t)
			extractionEvent(t, r, "source", guard.EntryOwner, "I enjoy hiking.")
			if err := r.queueExtraction(t.Context(), r.agent); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "revoked":
				if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), "UPDATE retract_meta SET epoch=epoch+1")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				extractionStep(t, r, s)
				if len(p.requests) != 0 {
					t.Fatal("obsolete input reached model")
				}
			case "failed", "malformed":
				p.steps = []turnStep{{err: errors.New("synthetic unavailable")}}
				if mode == "malformed" {
					p.steps = []turnStep{{events: turnText("not JSON")}}
				}
				req := extractionDue(t, r, s)
				if _, err := s.Aux(t.Context(), req); !errors.Is(err, jobapi.ErrUnavailable) {
					t.Fatalf("failed call: %v", err)
				}
				if _, err := s.Aux(t.Context(), req); !errors.Is(err, jobapi.ErrPending) {
					t.Fatalf("unknown call was replayed: %v", err)
				}
				if err := r.queueExtraction(t.Context(), r.agent); err != nil {
					t.Fatal(err)
				}
			}
			cursor, err := s.ExtractionCursor(t.Context(), extractionPrefix+r.agent)
			if err != nil || cursor != 0 {
				t.Fatalf("lost pending source: %d %v", cursor, err)
			}
			p.steps = append(p.steps, turnStep{events: turnText("[]")})
			extractionStep(t, r, s)
			cursor, err = s.ExtractionCursor(t.Context(), extractionPrefix+r.agent)
			if err != nil || cursor == 0 {
				t.Fatalf("retry did not commit source: %d %v", cursor, err)
			}
		})
	}
}

func TestMemoryExtractionRejectsWorkerForgeryAndDeletedAgent(t *testing.T) {
	r, p, s := extractionFixture(t)
	extractionEvent(t, r, "source", guard.EntryOwner, "I enjoy hiking.")
	if err := r.queueExtraction(t.Context(), r.agent); err != nil {
		t.Fatal(err)
	}
	p.steps = []turnStep{{events: turnText("[]")}}
	req := extractionDue(t, r, s)
	if _, err := s.Aux(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), jobapi.Proposal{Epoch: req.Epoch, JobID: req.JobID, From: req.From, Through: req.Through, Output: json.RawMessage(`[{}]`)}); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("forged result: %v", err)
	}
	cursor, err := s.ExtractionCursor(t.Context(), req.JobID)
	if err != nil || cursor != 0 {
		t.Fatal("forgery advanced progress")
	}
	if err := r.agents.Delete(t.Context(), r.agent, r.person, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := (growthJobAPI{r}).service(context.Background(), req.JobID); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("deleted agent service: %v", err)
	}
}

func TestMemoryExtractionInvalidModelEvidenceRemainsRetryable(t *testing.T) {
	r, p, s := extractionFixture(t)
	extractionEvent(t, r, "source", guard.EntryOwner, "I enjoy hiking.")
	if err := r.queueExtraction(t.Context(), r.agent); err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal([]memory.Item{{Kind: memory.Fact, Content: "Unsupported statement", Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "not-in-batch", Precision: memory.Exact}}}})
	p.steps = []turnStep{{events: turnText(string(output))}, {events: turnText("[]")}}
	if _, err := s.Aux(t.Context(), extractionDue(t, r, s)); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatalf("invalid evidence was cached instead of failing attempt: %v", err)
	}
	if err := r.queueExtraction(t.Context(), r.agent); err != nil {
		t.Fatal(err)
	}
	extractionStep(t, r, s)
}

func TestMemoryExtractionReadsMaterialBackedStatement(t *testing.T) {
	r, p, s := extractionFixture(t)
	statement := "I prefer tea. " + strings.Repeat("A synthetic detail. ", 6000)
	extractionEvent(t, r, "large-source", guard.EntryOwner, statement)
	if err := r.queueExtraction(t.Context(), r.agent); err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal([]memory.Item{{Kind: memory.Preference, Content: "Prefers tea", Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "large-source", Precision: memory.Exact}}}})
	p.steps = []turnStep{{events: turnText(string(output))}}
	extractionStep(t, r, s)
	hits, err := memory.New(r.db).Recall(t.Context(), memory.RecallScope{Agent: r.agent, Person: r.person}, memory.Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 1 || hits[0].Evidence[0].Quote != statement {
		t.Fatalf("material evidence lost: hits=%d err=%v", len(hits), err)
	}
}

func TestMemoryExtractionApplyFailureKeepsCursorAndCachedResult(t *testing.T) {
	r, p, s := extractionFixture(t)
	extractionEvent(t, r, "source", guard.EntryOwner, "I prefer tea and enjoy hiking.")
	if err := r.queueExtraction(t.Context(), r.agent); err != nil {
		t.Fatal(err)
	}
	var items []memory.Item
	for _, content := range []string{"Prefers tea", "Enjoys hiking"} {
		items = append(items, memory.Item{Kind: memory.Preference, Content: content, Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}})
	}
	output, _ := json.Marshal(items)
	p.steps = []turnStep{{events: turnText(string(output))}}
	req := extractionDue(t, r, s)
	aux, err := s.Aux(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_extraction BEFORE INSERT ON memory_item WHEN NEW.content='Enjoys hiking' BEGIN SELECT RAISE(ABORT,'injected'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proposal := jobapi.Proposal{Epoch: req.Epoch, JobID: req.JobID, From: req.From, Through: req.Through, Output: aux.Output}
	if _, err = s.Apply(t.Context(), proposal); err == nil {
		t.Fatal("missing injected write failure")
	}
	cursor, err := s.ExtractionCursor(t.Context(), req.JobID)
	if err != nil || cursor != 0 {
		t.Fatal("failed transaction advanced source")
	}
	hits, err := memory.New(r.db).Recall(t.Context(), memory.RecallScope{Agent: r.agent, Person: r.person}, memory.Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 0 {
		t.Fatalf("partial memory batch: %d %v", len(hits), err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DROP TRIGGER fail_extraction")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	extractionStep(t, r, s)
	if len(p.requests) != 1 {
		t.Fatal("Apply retry repeated model call")
	}
}
