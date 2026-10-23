// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/moirai"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

func preparationService(t *testing.T, b *Builder, db *canon.Store) (*jobs.Service, jobapi.Job) {
	t.Helper()
	b.PreparationJob = "prepare-conversation"
	job := jobapi.Job{ID: b.PreparationJob, Kind: jobapi.Memory, Enabled: true, Every: time.Minute, Timeout: time.Second, Budget: 100, MaxTokens: 10, Adoption: "prepared-summary"}
	service, err := jobs.New(jobs.Config{Store: db, Epoch: 1, Limit: 100, Definitions: map[string]jobs.Definition{job.ID: {
		Agent: b.Agent, Account: "synthetic", Provider: provider.ChatGPT,
		Model: func(ctx context.Context, c jobs.Call) (jobs.Result, error) {
			raw, err := b.GeneratePreparation(ctx, c.Input)
			return jobs.Result{Output: raw}, err
		}, Apply: b.ApplyPreparation,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Register(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	return service, job
}
func enqueuePreparation(t *testing.T, b *Builder, v View, s *jobs.Service, j jobapi.Job) PassSegment {
	t.Helper()
	raw, err := b.PreparationTask(t.Context(), PassSegment{Records: v.History, Protected: []Record{v.Input}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Enqueue(t.Context(), j.ID, jobapi.Task{Sequence: 1, Input: raw}); err != nil {
		t.Fatal(err)
	}
	return PassSegment{Records: v.History, Protected: []Record{v.Input}}
}
func TestWorkerPreparationCommitsWithoutPublishingAndRestartFallsBack(t *testing.T) {
	t.Parallel()
	b, v, db := builderFixture(t)
	calls := 0
	generate := b.Generate
	b.Generate = func(ctx context.Context, s PassSegment) (Text, error) { calls++; return generate(ctx, s) }
	service, job := preparationService(t, b, db)
	segment := enqueuePreparation(t, b, v, service, job)
	if _, ready, err := b.Prepared(t.Context(), segment); err != nil || ready {
		t.Fatalf("unstarted ready=%v err=%v", ready, err)
	}
	if err := (moirai.Worker{Core: service}).Step(t.Context(), jobapi.Work{Epoch: 1, Job: job, Task: jobapi.Task{Sequence: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := counts(t, db); !reflect.DeepEqual(got, []int{0, 0, 0}) {
		t.Fatalf("preparation published: %v", got)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT output FROM moirai_aux UNION ALL SELECT proposal FROM moirai_applied")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err = rows.Scan(&data); err != nil {
				return err
			}
			if bytes.Contains(data, []byte("reservation")) || bytes.Contains(data, []byte("omitted_detail")) {
				t.Fatal("durable receipt contains derived text")
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	// Restart loses the speculative cache, as in the reference. The durable
	// receipt alone must not carry derived conversation text.
	fresh := &Builder{Store: b.Store, Events: b.Events, Agent: b.Agent, Conversation: b.Conversation, Model: b.Model, Now: b.Now, NextID: b.NextID, Generate: b.Generate, PreparationJob: job.ID}
	addEvent(t, db, "steering", "conversation")
	segment.Protected = append(segment.Protected, Record{ID: "steering"})
	_, ready, err := fresh.Prepared(t.Context(), segment)
	if err != nil || ready {
		t.Fatalf("restart revived preparation: ready=%v err=%v", ready, err)
	}
	summary, ready, err := b.Prepared(t.Context(), segment)
	if err != nil || !ready || calls != 1 {
		t.Fatalf("ready=%v calls=%d err=%v", ready, calls, err)
	}
	if _, err = b.Publish(t.Context(), segment, summary); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("foreground regenerated preparation")
	}
}
func TestWorkerPreparationRejectsRacesAndForgedOutput(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"epoch", "append", "competing root", "forged", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			b, v, db := builderFixture(t)
			service, job := preparationService(t, b, db)
			segment := enqueuePreparation(t, b, v, service, job)
			aux, err := service.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: job.ID, Through: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, ready, err := b.Prepared(t.Context(), segment); err != nil || ready {
				t.Fatalf("unapplied Aux visible: %v %v", ready, err)
			}
			var want error
			switch kind {
			case "epoch":
				write(t, db, "UPDATE retract_meta SET epoch=epoch+1")

			case "append":
				addEvent(t, db, "late", "conversation")
			case "competing root":
				text, err := b.Summarize(t.Context(), segment)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = b.Publish(t.Context(), segment, text); err != nil {
					t.Fatal(err)
				}
			case "forged":
				var r preparationTicket
				if err = json.Unmarshal(aux.Output, &r); err != nil {
					t.Fatal(err)
				}
				r.ID = "forged"
				aux.Output, _ = json.Marshal(r)
				want = ErrSummary
			case "rollback":
				write(t, db, `CREATE TRIGGER reject_preparation BEFORE INSERT ON moirai_applied BEGIN SELECT RAISE(ABORT,'injected'); END;`)
			}
			_, err = service.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: job.ID, Through: 1, Output: aux.Output})
			if kind == "rollback" {
				if err == nil {
					t.Fatal("rollback ignored")
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("apply=%v want=%v", err, want)
			}
			expectedReceipts := 1
			if kind == "forged" || kind == "rollback" {
				expectedReceipts = 0
			}
			var receipts int
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM moirai_applied").Scan(&receipts)
			}); err != nil || receipts != expectedReceipts {
				t.Fatalf("receipts=%d %v", receipts, err)
			}
		})
	}
}
func TestWorkerPreparedReceiptRejectsChangedSourcesEpochAndConfiguration(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"source selection", "epoch", "model", "generation revision", "root revision"} {
		t.Run(kind, func(t *testing.T) {
			b, v, db := builderFixture(t)
			if kind == "root revision" {
				segment := PassSegment{Records: v.History, Protected: []Record{v.Input}}
				text, err := b.Summarize(t.Context(), segment)
				if err != nil {
					t.Fatal(err)
				}
				root, err := b.Publish(t.Context(), segment, text)
				if err != nil {
					t.Fatal(err)
				}
				v.RootID, v.Root, v.History = root.ID, root.Item, nil
			}
			service, job := preparationService(t, b, db)
			segment := PassSegment{RootID: v.RootID, Root: v.Root, Records: v.History, Protected: []Record{v.Input}}
			raw, err := b.PreparationTask(t.Context(), segment)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.Enqueue(t.Context(), job.ID, jobapi.Task{Sequence: 1, Input: raw}); err != nil {
				t.Fatal(err)
			}
			if err = (moirai.Worker{Core: service}).Step(t.Context(), jobapi.Work{Epoch: 1, Job: job, Task: jobapi.Task{Sequence: 1}}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "source selection":
				segment.Records, segment.Protected = segment.Protected, segment.Records
			case "epoch":
				write(t, db, "UPDATE retract_meta SET epoch=epoch+1")
			case "model":
				b.Model = "changed"
			case "generation revision":
				b.GenerationRevision = "changed-reasoning"
			case "root revision":
				write(t, db, "UPDATE sum_node SET revision=revision+1")
			}
			_, ready, err := b.Prepared(t.Context(), segment)
			var want error
			if kind == "source selection" {
				want = ErrCoverage // The changed selection is no longer a prefix.
			}
			if !errors.Is(err, want) || ready {
				t.Fatalf("stale preparation ready=%v err=%v", ready, err)
			}
		})
	}
}
func TestAfterDeliveryPreparationUsesForegroundSelectionAndNoPeriodicRollup(t *testing.T) {
	t.Parallel()
	b, v, db := builderFixture(t)
	b.PreparationJob = "prepare"
	p := Pass{Policy: Policy{ContextWindow: 1500, KeepUserTokens: 100}, Count: countBytes, Summarize: b.Summarize, Prepared: b.Prepared, Publish: b.Publish}
	var task json.RawMessage
	queued, err := b.QueuePreparation(t.Context(), p, v, func(_ context.Context, raw json.RawMessage) error { task = raw; return nil })
	if err != nil || !queued {
		t.Fatalf("queued=%v %v", queued, err)
	}
	var input preparationInput
	if err = json.Unmarshal(task, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Segment.Records) != 1 || input.Segment.Records[0].ID != "fact" || len(input.Segment.Verbatim) != 1 || input.Segment.Verbatim[0].ID != "input" || len(input.Segment.Protected) != 0 {
		t.Fatalf("segment=%+v", input.Segment)
	}
	if got := counts(t, db); !reflect.DeepEqual(got, []int{0, 0, 0}) {
		t.Fatal(got)
	}
	p.Policy.ContextWindow = 100000
	if queued, err = b.QueuePreparation(t.Context(), p, v, func(context.Context, json.RawMessage) error { t.Fatal("below threshold enqueued"); return nil }); err != nil || queued {
		t.Fatalf("queued=%v %v", queued, err)
	}
}
func TestInFlightWorkerNeverHoldsForeground(t *testing.T) {
	t.Parallel()
	b, v, db := builderFixture(t)
	service, job := preparationService(t, b, db)
	segment := enqueuePreparation(t, b, v, service, job)
	started, release := make(chan struct{}), make(chan struct{})
	generate := b.Generate
	b.Generate = func(ctx context.Context, s PassSegment) (Text, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return Text{}, ctx.Err()
		}
		return generate(ctx, s)
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: job.ID, Through: 1})
		done <- err
	}()
	<-started
	// No sleep or timing-based assertion: the model is held until Prepared returns.
	_, ready, err := b.Prepared(t.Context(), segment)
	close(release)
	auxErr := <-done
	if err != nil || ready || auxErr != nil {
		t.Fatalf("ready=%v prepared=%v aux=%v", ready, err, auxErr)
	}
}

func TestObsoletePreparationDoesNotBlockNextTask(t *testing.T) {
	t.Parallel()
	b, v, db := builderFixture(t)
	service, job := preparationService(t, b, db)
	segment := enqueuePreparation(t, b, v, service, job)
	addEvent(t, db, "new-input", "conversation")
	worker := moirai.Worker{Core: service}
	if err := worker.Step(t.Context(), jobapi.Work{Epoch: 1, Job: job, Task: jobapi.Task{Sequence: 1}}); err != nil {
		t.Fatalf("obsolete preparation blocks job: %v", err)
	}
	segment.Protected = append(segment.Protected, Record{ID: "new-input"})
	raw, err := b.PreparationTask(t.Context(), segment)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Enqueue(t.Context(), job.ID, jobapi.Task{Sequence: 2, Input: raw}); err != nil {
		t.Fatal(err)
	}
	// Advance durable due time directly, not by sleeping.
	write(t, db, `UPDATE moirai_jobs SET record=json_set(record,'$.NextRun','2000-01-01T00:00:00Z') WHERE id=?`, job.ID)
	job.Watermark = 1
	if err = worker.Step(t.Context(), jobapi.Work{Epoch: 1, Job: job, Task: jobapi.Task{Sequence: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := b.Prepared(t.Context(), segment); err != nil || !ready {
		t.Fatalf("next task ready=%v err=%v", ready, err)
	}
}
