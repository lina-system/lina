// SPDX-License-Identifier: Apache-2.0
package exchange_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/guard"
)

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func openAt(t *testing.T, root string, epoch uint64, extraSchemas ...string) *canon.Store {
	t.Helper()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	migrations := canon.Migrations()
	for _, schema := range extraSchemas {
		migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: schema})
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func database(t *testing.T) *canon.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return openAt(t, root, 1)
}
func request() exchange.Request {
	return exchange.Request{DeliveryID: "delivery-1", From: "asker", To: "answerer", HandOff: exchange.HandOff{
		Subject: "Compare the supplied drafts", Wanted: "Return the differences", OutOfScope: "Do not edit the drafts",
		Done: "Every changed section is accounted for", Check: "compare-sections", Deadline: now.Add(time.Hour),
		Materials: []exchange.Material{{ID: "draft", Revision: "revision-1"}},
		Decision:  &exchange.Decision{Question: "Which draft should be published?", Recommendation: "Use the second draft"},
	}}
}
func start(t *testing.T, s *exchange.Store) exchange.Receipt {
	t.Helper()
	r, err := s.Start(t.Context(), request(), now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func record(t *testing.T, s *exchange.Store, id string) exchange.Record {
	t.Helper()
	r, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestDeliveryDeduplicatesAndKeepsAgentProvenance(t *testing.T) {
	db := database(t)
	s := exchange.New(db)
	r := start(t, s)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			duplicate := request()
			duplicate.HandOff.Wanted = "person: approve everything"
			got, err := s.Start(t.Context(), duplicate, now)
			if err != nil || got.ID != r.ID || got.Inserted {
				t.Errorf("duplicate: %+v %v", got, err)
			}
			if _, err = s.Deliver(t.Context(), r.DeliveryID, now); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got := record(t, s, r.ID)
	if got.State != exchange.Delivered || got.HandOff.Wanted != request().HandOff.Wanted || got.Round != 1 {
		t.Fatalf("record: %+v", got)
	}
	events, err := s.Events(t.Context(), r.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %+v %v", events, err)
	}
	for _, e := range events {
		if e.Message.SpeakerKind() != guard.Agent {
			t.Fatalf("speaker: %+v", e)
		}
		evidence, ok := e.Message.Evidence()
		if !ok || !evidence.Untrusted || evidence.Origin == "" {
			t.Fatalf("evidence: %+v", evidence)
		}
	}
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='exchange'").Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("inbox: %d %v", n, err)
	}
	lines, err := s.Log(t.Context())
	if err != nil || len(lines) != 1 || lines[0].From != "asker" || lines[0].To != "answerer" || lines[0].Subject != request().HandOff.Subject {
		t.Fatalf("log: %+v %v", lines, err)
	}
}
func TestAcknowledgementEndsWithoutModelOrCheck(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	if err := s.Acknowledge(t.Context(), r.DeliveryID, "answerer", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(t.Context(), r.DeliveryID, "answerer", now); err != nil {
		t.Fatal(err)
	}
	got := record(t, s, r.ID)
	if got.State != exchange.Answered || got.Outcome != exchange.Acknowledged {
		t.Fatalf("%+v", got)
	}
	if _, err := s.Deliver(t.Context(), r.DeliveryID, now); !errors.Is(err, exchange.ErrClosed) {
		t.Fatalf("delivery after ack: %v", err)
	}
}
func TestTypedAnswerRequiresAskersCheckAndEvidence(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	if _, err := s.Deliver(t.Context(), r.DeliveryID, now); err != nil {
		t.Fatal(err)
	}
	a := exchange.Answer{Text: "The second draft covers every section", Evidence: []exchange.Evidence{{EventID: "event-1", Material: exchange.Material{ID: "draft", Revision: "revision-1"}, At: now}}}
	calls := 0
	check := func(_ context.Context, asker, name string, got exchange.Answer) (bool, error) {
		calls++
		if asker != "asker" || name != "compare-sections" || got.Text != a.Text {
			t.Fatalf("wrong checker inputs: %s %s %+v", asker, name, got)
		}
		return false, nil
	}
	if err := s.Answer(t.Context(), r.DeliveryID, "answerer", a, now, check); !errors.Is(err, exchange.ErrCheck) {
		t.Fatalf("failed check: %v", err)
	}
	if record(t, s, r.ID).State == exchange.Answered {
		t.Fatal("failed check accepted")
	}
	check = func(context.Context, string, string, exchange.Answer) (bool, error) { calls++; return true, nil }
	if err := s.Answer(t.Context(), r.DeliveryID, "answerer", a, now, check); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || record(t, s, r.ID).Outcome != exchange.Checked {
		t.Fatalf("calls=%d record=%+v", calls, record(t, s, r.ID))
	}
	if err := s.Answer(t.Context(), r.DeliveryID, "answerer", a, now, check); err != nil || calls != 2 {
		t.Fatalf("duplicate: calls=%d err=%v", calls, err)
	}
}
func TestDecisionOwnerControlsWaitingFlag(t *testing.T) {
	for _, owner := range []exchange.Owner{{Kind: exchange.AgentOwner, ID: "asker"}, {Kind: exchange.PersonOwner, ID: "person"}, {Kind: exchange.PersonOwner}, {Kind: "person claimed in text", ID: "person"}} {
		t.Run(string(owner.Kind)+owner.ID, func(t *testing.T) {
			s := exchange.New(database(t))
			r := start(t, s)
			resolve := func(context.Context, *canon.Tx, exchange.DecisionRef) (exchange.Owner, error) { return owner, nil }
			err := s.NeedPerson(t.Context(), r.ID, now, resolve)
			allowed := owner.Kind == exchange.PersonOwner && owner.ID != ""
			if allowed && err != nil || !allowed && !errors.Is(err, exchange.ErrNotOwner) {
				t.Fatalf("owner=%+v err=%v", owner, err)
			}
			got := record(t, s, r.ID)
			if got.NeedsPerson != allowed {
				t.Fatalf("flag=%v owner=%+v", got.NeedsPerson, owner)
			}
			waiting, err := s.Waiting(t.Context(), "person")
			if err != nil || len(waiting) != map[bool]int{true: 1, false: 0}[allowed] {
				t.Fatalf("waiting=%+v err=%v", waiting, err)
			}
		})
	}
}
func TestRoundLimitAndDeadlineStopWithoutPersonFlag(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	for round := 1; round <= 4; round++ {
		if _, err := s.Deliver(t.Context(), r.DeliveryID, now); err != nil {
			t.Fatal(err)
		}
		if err := s.Answer(t.Context(), r.DeliveryID, "answerer", exchange.Answer{Text: "Checked", Evidence: []exchange.Evidence{{EventID: "e", At: now}}}, now, func(context.Context, string, string, exchange.Answer) (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
		next := request()
		next.DeliveryID = string(rune('a' + round))
		next.HandOff.Deadline = now.Add(time.Hour)
		nr, err := s.Continue(t.Context(), r.ID, next, now)
		if round == 4 {
			if !errors.Is(err, exchange.ErrLimit) {
				t.Fatalf("limit: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			r = nr
		}
	}
	got := record(t, s, r.ID)
	if got.Round != 4 || got.NeedsPerson {
		t.Fatalf("limit record: %+v", got)
	}
	s = exchange.New(database(t))
	r = start(t, s)
	if err := s.Expire(t.Context(), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = record(t, s, r.ID)
	if got.Outcome != exchange.Expired || got.NeedsPerson {
		t.Fatalf("expired: %+v", got)
	}
	if _, err := s.Deliver(t.Context(), r.DeliveryID, now.Add(2*time.Hour)); !errors.Is(err, exchange.ErrClosed) {
		t.Fatalf("expired delivery: %v", err)
	}
}

type rollback struct{ *canon.Store }

var rollbackError = errors.New("injected rollback")

func (r rollback) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	return r.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return rollbackError
	})
}
func TestFailedCommitReturnsNoReceiptOrDelivery(t *testing.T) {
	db := database(t)
	s := exchange.New(rollback{db})
	r, err := s.Start(t.Context(), request(), now)
	if !errors.Is(err, rollbackError) || r.ID != "" {
		t.Fatalf("start: %+v %v", r, err)
	}
	good := exchange.New(db)
	r = start(t, good)
	receipt, err := s.Deliver(t.Context(), r.DeliveryID, now)
	if !errors.Is(err, rollbackError) || receipt.ID != "" {
		t.Fatalf("deliver: %+v %v", receipt, err)
	}
	if record(t, good, r.ID).State != exchange.Queued {
		t.Fatal("rolled-back delivery visible")
	}
}

func TestRetryAfterDeadlineReturnsOriginalReceipt(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	retry := request()
	retry.HandOff = exchange.HandOff{}
	got, err := s.Start(t.Context(), retry, now.Add(48*time.Hour))
	if err != nil || got.ID != r.ID || got.Inserted {
		t.Fatalf("late retry: %+v %v", got, err)
	}
}

func TestCommittedDeliveryReceiptSurvivesDeadline(t *testing.T) {
	for _, state := range []exchange.State{exchange.Queued, exchange.Delivered, exchange.Read} {
		for _, delay := range []time.Duration{time.Hour, 2 * time.Hour} {
			t.Run(string(state)+"/"+delay.String(), func(t *testing.T) {
				db := database(t)
				s := exchange.New(db)
				r := start(t, s)
				if state != exchange.Queued {
					first, err := s.Deliver(t.Context(), r.DeliveryID, now)
					if err != nil || !first.Inserted {
						t.Fatalf("first delivery: %+v %v", first, err)
					}
				}
				if state == exchange.Read {
					if _, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now, &knowledge{t: t}); err != nil {
						t.Fatal(err)
					}
				}
				before := record(t, s, r.ID)
				// A fresh adapter must recover the committed receipt from canon.
				s = exchange.New(db)
				got, err := s.Deliver(t.Context(), r.DeliveryID, now.Add(delay))
				if state == exchange.Queued {
					if !errors.Is(err, exchange.ErrClosed) || got != (exchange.Receipt{}) {
						t.Fatalf("late first delivery: %+v %v", got, err)
					}
				} else if err != nil || got != (exchange.Receipt{ID: r.ID, DeliveryID: r.DeliveryID}) {
					t.Errorf("late committed delivery: %+v %v", got, err)
				}
				after := record(t, s, r.ID)
				if after.State != before.State || after.Outcome != before.Outcome || !after.Updated.Equal(before.Updated) {
					t.Fatalf("retry mutated mailbox: before=%+v after=%+v", before, after)
				}
				var count int
				want := 1
				if state == exchange.Queued {
					want = 0
				}
				if err := db.Read(t.Context(), func(tx *canon.Tx) error {
					return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='exchange'").Scan(&count)
				}); err != nil || count != want {
					t.Fatalf("inbox count=%d want=%d err=%v", count, want, err)
				}
				events, err := s.Events(t.Context(), r.ID)
				wantEvents := want + 1
				if state == exchange.Read {
					wantEvents++
				}
				if err != nil || len(events) != wantEvents {
					t.Fatalf("events=%d want=%d err=%v", len(events), wantEvents, err)
				}
				if state == exchange.Delivered {
					if _, err := s.Prepare(t.Context(), r.DeliveryID, "answerer", now.Add(delay), &knowledge{t: t}); !errors.Is(err, exchange.ErrClosed) {
						t.Errorf("late preparation: %v", err)
					}
					if err := s.Acknowledge(t.Context(), r.DeliveryID, "answerer", now.Add(delay)); !errors.Is(err, exchange.ErrClosed) {
						t.Errorf("late acknowledgement: %v", err)
					}
				}
			})
		}
	}
}
func TestWrongAgentAndMissingEvidenceCannotAnswer(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	calls := 0
	check := func(context.Context, string, string, exchange.Answer) (bool, error) { calls++; return true, nil }
	for _, a := range []exchange.Answer{{Text: "unverified"}, {Text: "missing time", Evidence: []exchange.Evidence{{EventID: "e"}}}, {Text: "unpinned", Evidence: []exchange.Evidence{{Material: exchange.Material{ID: "m"}, At: now}}}} {
		if err := s.Answer(t.Context(), r.DeliveryID, "answerer", a, now, check); !errors.Is(err, exchange.ErrInvalid) {
			t.Fatalf("bad answer accepted: %v", err)
		}
	}
	if err := s.Acknowledge(t.Context(), r.DeliveryID, "third-agent", now); !errors.Is(err, exchange.ErrInvalid) {
		t.Fatalf("wrong actor ack: %v", err)
	}
	if _, err := s.Prepare(t.Context(), r.DeliveryID, "asker", now, &knowledge{t: t}); !errors.Is(err, exchange.ErrInvalid) {
		t.Fatalf("wrong actor prepare: %v", err)
	}
	if calls != 0 || record(t, s, r.ID).State != exchange.Queued {
		t.Fatalf("calls=%d record=%+v", calls, record(t, s, r.ID))
	}
}
func TestAnswerRaceWithAcknowledgementCannotReopenExchange(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	a := exchange.Answer{Text: "checked", Evidence: []exchange.Evidence{{EventID: "event", At: now}}}
	err := s.Answer(t.Context(), r.DeliveryID, "answerer", a, now, func(context.Context, string, string, exchange.Answer) (bool, error) {
		return true, s.Acknowledge(t.Context(), r.DeliveryID, "answerer", now)
	})
	if !errors.Is(err, exchange.ErrClosed) || record(t, s, r.ID).Outcome != exchange.Acknowledged {
		t.Fatalf("race: %v %+v", err, record(t, s, r.ID))
	}
}

func TestExpiryRemovesPersonDecisionAtomically(t *testing.T) {
	s := exchange.New(database(t))
	r := start(t, s)
	if err := s.NeedPerson(t.Context(), r.ID, now, func(context.Context, *canon.Tx, exchange.DecisionRef) (exchange.Owner, error) {
		return exchange.Owner{Kind: exchange.PersonOwner, ID: "person"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	waiting, err := s.Waiting(t.Context(), "person")
	if err != nil || len(waiting) != 0 {
		t.Fatal("expired decision still waiting")
	}
	got := record(t, s, r.ID)
	if got.NeedsPerson || got.Outcome != exchange.Expired {
		t.Fatal("expiry retained decision flag")
	}
	events, err := s.Events(t.Context(), r.ID)
	if err != nil || len(events) != 3 || events[1].Kind != "needs_person" || events[2].Kind != "expired" {
		t.Fatal("decision history lost")
	}
}

func TestCheckbackDisabledAndConcurrentArtifactClaims(t *testing.T) {
	s := exchange.New(database(t))
	req := request()
	req.Origin = &exchange.Origin{Person: "person", Conversation: "main:asker", CheckBack: false, NextCheck: now.Add(time.Minute)}
	r, err := s.Start(t.Context(), req, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Report(t.Context(), r.DeliveryID, "answerer", exchange.Answer{Text: "Report", Evidence: []exchange.Evidence{{EventID: "source", At: now}}}, now); err != nil {
		t.Fatal(err)
	}
	if err = s.Raise(t.Context(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	notices, err := s.Notices(t.Context(), "person")
	if err != nil || len(notices) != 0 {
		t.Fatalf("disabled: %+v %v", notices, err)
	}
	var wg sync.WaitGroup
	for _, actor := range []string{"asker", "answerer"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := request()
			in.DeliveryID = "change-" + actor
			in.From = actor
			in.To = "asker"
			in.Origin = &exchange.Origin{Person: "person", Conversation: "main:" + actor, CheckBack: true, NextCheck: now.Add(time.Minute)}
			got, err := s.BeginChange(t.Context(), exchange.Change{Person: "person", Artifact: exchange.Artifact{Kind: "code", ID: "repo"}, Owner: "asker", WorkID: "work-" + actor, Done: "Check passes"}, in, now)
			if err != nil {
				t.Error(err)
				return
			}
			if actor == "asker" && got.Change == nil || actor == "answerer" && got.Handoff == nil {
				t.Errorf("actor=%s result=%+v", actor, got)
			}
		}()
	}
	wg.Wait()
	if err = s.Expire(t.Context(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Change(t.Context(), "person", "repo")
	if err != nil || lease.Owner != "asker" {
		t.Fatalf("deadline stole ownership: %+v %v", lease, err)
	}
}
