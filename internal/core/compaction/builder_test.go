// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func builderFixture(t *testing.T) (*Builder, View, *canon.Store) {
	db, _ := passDB(t)
	events := ledger.New(db, nil)
	var history []Record
	for _, id := range []string{"fact", "input"} {
		text := "계속해 주세요"
		if id == "fact" {
			text = "예약 번호는 42입니다. " + strings.Repeat("context ", 300)
		}
		e, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "conversation", PersonID: "owner", AgentID: "agent", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Body: text})
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, Record{ID: id, Item: message(e.Body.DerivedText()), User: true})
	}
	n := 0
	b := &Builder{Store: New(db), Events: events, Agent: "agent", Conversation: "conversation", Model: "model", Now: func() time.Time { return time.Unix(2, 0) }, NextID: func() string { n++; return "summary-" + strings.Repeat("x", n) }, Generate: func(context.Context, PassSegment) (Text, error) {
		return Text{Requests: "continue", Topics: "reservation", Actions: "none", Values: "42", Omitted: "wording"}, nil
	}}
	v := View{Request: provider.Request{Model: "model", Purpose: provider.Turn}, History: history[:1], Input: history[1]}
	return b, v, db
}
func TestStoredSummaryFailurePublishesNothingAndPreservesRequest(t *testing.T) {
	b, v, db := builderFixture(t)
	failed := errors.New("unavailable")
	b.Generate = func(context.Context, PassSegment) (Text, error) { return Text{}, failed }
	p := Pass{Policy: Policy{ContextWindow: 1500, KeepUserTokens: 100}, Count: countBytes, Summarize: b.Summarize, Prepared: b.Prepared, Publish: b.Publish}
	got, err := p.Fit(t.Context(), v, false)
	if !errors.Is(err, failed) || !reflect.DeepEqual(got.View.History, v.History) || !reflect.DeepEqual(counts(t, db), []int{0, 0, 0}) {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestPublishedNodeExpandsExactFactThroughTool(t *testing.T) {
	b, v, db := builderFixture(t)
	p := Pass{Policy: Policy{ContextWindow: 1500, KeepUserTokens: 100}, Count: countBytes, Summarize: b.Summarize, Prepared: b.Prepared, Publish: b.Publish}
	got, err := p.Fit(t.Context(), v, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.View.History) != 0 || got.View.RootID == "" {
		t.Fatal("not compacted")
	}
	expansion := Expansion{DB: db, Events: b.Events, NodeEvents: b.Store.ExpandEvents}
	spec, handler := coreexec.ExpandTool("agent", expansion.Read)
	args, _ := json.Marshal(map[string]any{"id": "node:" + got.View.RootID, "limit": 1})
	in, err := spec.Schema(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handler(t.Context(), lease.Lease{}, in)
	if result.State != tools.Applied {
		t.Fatalf("expansion state: %v", result.State)
	}
	raw := result.Body
	if err != nil {
		t.Fatal(err)
	}
	var page ExpansionPage
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	event, _ := b.Events.Get(t.Context(), "fact")
	if len(page.Events) != 1 || page.Events[0].ID != "fact" || page.Events[0].Body != event.Body.Text() {
		t.Fatalf("not exact: %s", raw)
	}
	var audit int
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM conv_event WHERE scaffold_kind='compaction_recorded'`).Scan(&audit)
	}); err != nil || audit != 1 {
		t.Fatalf("audit=%d err=%v", audit, err)
	}
}

func TestPreparedSummarySurvivesUnchangedSourcesWithNewVerbatimInput(t *testing.T) {
	b, v, db := builderFixture(t)
	calls := 0
	b.Generate = func(context.Context, PassSegment) (Text, error) {
		calls++
		return Text{Requests: "continue", Topics: "reservation", Actions: "none", Values: "42", Omitted: "wording"}, nil
	}
	segment := PassSegment{Records: v.History, Protected: []Record{v.Input}}
	if _, err := b.Summarize(t.Context(), segment); err != nil {
		t.Fatal(err)
	}
	addEvent(t, db, "steering", "conversation")
	segment.Protected = append(segment.Protected, Record{ID: "steering", Item: message("synthetic steering")})
	summary, ready, err := b.Prepared(t.Context(), segment)
	if err != nil || !ready || calls != 1 {
		t.Fatalf("ready=%v calls=%d err=%v", ready, calls, err)
	}
	if _, err = b.Publish(t.Context(), segment, summary); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("prepared summary requested again")
	}
}
func TestPublicationAndAuditFailAtomically(t *testing.T) {
	b, v, db := builderFixture(t)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_compaction_audit BEFORE INSERT ON conv_event WHEN NEW.scaffold_kind='compaction_recorded' BEGIN SELECT RAISE(ABORT,'audit_failed'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := Pass{Policy: Policy{ContextWindow: 1500, KeepUserTokens: 100}, Count: countBytes, Summarize: b.Summarize, Publish: b.Publish}
	got, err := p.Fit(t.Context(), v, false)
	if err == nil || !reflect.DeepEqual(got.View.History, v.History) || !reflect.DeepEqual(counts(t, db), []int{0, 0, 0}) {
		t.Fatalf("partial publication: %v %+v", err, got)
	}
}
func TestNodeExpansionPagesDescendantsInLedgerOrderAndRejectsOtherAgent(t *testing.T) {
	db, s, _, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	publish(t, s, Proposal{ID: "parent", Expected: frontier(t, s), Children: []string{"leaf"}, Cover: []Coverage{{EventID: "e2"}}, Output: output("combined")})
	ids, next, err := s.ExpandEvents(t.Context(), "agent", "parent", 0, 1)
	if err != nil || next != 1 || !reflect.DeepEqual(ids, []string{"e1"}) {
		t.Fatalf("%v %d %v", ids, next, err)
	}
	ids, next, err = s.ExpandEvents(t.Context(), "agent", "parent", next, 1)
	if err != nil || next != 0 || !reflect.DeepEqual(ids, []string{"e2"}) {
		t.Fatalf("%v %d %v", ids, next, err)
	}
	if ids, _, err = s.ExpandEvents(t.Context(), "other", "parent", 99, 1); err == nil || len(ids) != 0 {
		t.Fatal("foreign empty page disclosed")
	}
	if err = db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStoredContinuationOverThresholdRetainsLineageAndSizes(t *testing.T) {
	b, v, db := builderFixture(t)
	v.Fixed = []provider.Item{message("fixed")}
	v.Packet = []provider.Item{message("packet")}
	p := Pass{Policy: Policy{ContextWindow: 100}, Count: countBytes, Summarize: b.Summarize, Publish: b.Publish}
	ended := 0
	journal := ContinuationLedger{DB: db, Agent: "agent", Conversation: "conversation", EventID: "continuation", SizesID: "sizes", At: time.Unix(3, 0), End: func(context.Context, *canon.Tx, string) error { ended++; return nil }}
	out, err := p.Continue(t.Context(), v, Continuation{EndedTurn: "turn", InputID: "input", Record: journal.Record, Packet: func(context.Context) ([]provider.Item, error) { return v.Packet, nil }, Log: journal.Log, Send: func(context.Context, provider.Request) error { t.Fatal("oversize continuation dispatched"); return nil }})
	if !errors.Is(err, ErrTooLarge) || ended != 1 || len(out.Request.Items) != 0 || out.Sizes.Total < out.Sizes.Threshold {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err = b.Events.Get(t.Context(), "continuation"); err != nil {
		t.Fatal(err)
	}
	event, err := b.Events.Get(t.Context(), "sizes")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Sizes Sizes `json:"sizes"`
	}
	if err = json.Unmarshal([]byte(event.Body.Text()), &record); err != nil || record.Sizes != out.Sizes {
		t.Fatalf("sizes differ: %s %v", event.Body.Text(), err)
	}
	if len(counts(t, db)) != 3 || counts(t, db)[0] != 1 {
		t.Fatal("continuation summary not durable")
	}
}
