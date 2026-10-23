// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestInputBindingAdmission(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now().UTC()
	state := intakereplay.State{Clock: now, Focus: intakereplay.Target{Class: "none"}, Items: []intakereplay.Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, intakereplay.Input{}); err != nil {
		t.Fatal(err)
	}
	// Stop before model dispatch, after the actual authenticated admission path.
	r.intakeReplay = func(context.Context, ledger.Event) error { return nil }
	id, text := "one", "Garden"
	for range 2 {
		if _, err := r.Input(t.Context(), clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Input{InputID: &id, Text: &text}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var raw string
	err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM conv_event b JOIN conv_event p ON b.parent_id=p.id WHERE b.scaffold_kind='input_binding' AND json_extract(p.origin,'$.locator')=?`, id).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("binding count = %d, want exactly one", count)
			return nil
		}
		return tx.QueryRowContext(t.Context(), `SELECT b.body FROM conv_event b JOIN conv_event p ON b.parent_id=p.id WHERE b.scaffold_kind='input_binding' AND json_extract(p.origin,'$.locator')=?`, id).Scan(&raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		return
	}
	var got struct {
		Target  intakereplay.Target `json:"target"`
		Decider string              `json:"decider"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Target.ID != "garden" || got.Decider != "exact-reference" {
		t.Fatalf("binding: %s", raw)
	}
}

func TestInputBindingStateAndPacket(t *testing.T) {
	r := controlRuntime(t)
	ctx := t.Context()
	now := time.Now().UTC()
	s := intakereplay.State{Clock: now, Focus: intakereplay.Target{Class: "none"}, Items: []intakereplay.Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}, {ID: "library", Title: "Library", Kind: "project", State: "active", LastActivity: now.Add(-time.Hour)}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(ctx, s, intakereplay.Input{}); err != nil {
		t.Fatal(err)
	}
	admit := func(id, text string, at time.Time, surface ledger.Surface) ledger.Event {
		t.Helper()
		e, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Surface: surface, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: text})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	read := func(e ledger.Event) binding.Decision {
		t.Helper()
		saved, err := r.journal().Get(ctx, inputBindingID(e.ID))
		if err != nil {
			t.Fatal(err)
		}
		var d binding.Decision
		if err = json.Unmarshal([]byte(saved.Body.Text()), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	surface := ledger.Surface{Kind: "tui"}
	first := admit("first", "Garden", now, surface)
	if d := read(first); d.Target.ID != "garden" || d.Decider != "exact-reference" {
		t.Fatal(d)
	}
	aged := admit("aged", "continue", now.Add(time.Hour), surface)
	if d := read(aged); d.Target.Class != "none" || !d.Unresolved || d.FocusFresh || d.Candidates[0].ID != "garden" {
		t.Fatal(d)
	}
	replySurface := surface
	replySurface.ReplyTo = first.ID
	replied := admit("reply", "Library", now.Add(time.Hour+time.Second), replySurface)
	if d := read(replied); d.Target.ID != "garden" || d.Decider != "reply-or-card" {
		t.Fatal(d)
	}
	q := waiting.ConversationQuestion{ID: "format", Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "asking", Input: replied.ID, AskedAt: now.Add(time.Hour), QuestionDraft: waiting.QuestionDraft{Question: "Format?", Recommendation: "table", Choices: []string{"table", "list"}, Default: "table"}}
	if err := waiting.NewConversationQuestions(r.db).Ask(ctx, q, 2); err != nil {
		t.Fatal(err)
	}
	answer := admit("answer", "2", now.Add(time.Hour+2*time.Second), surface)
	if d := read(answer); d.Target.ID != "garden" || d.Decider != "offered-option" {
		t.Fatal(d)
	}
	packet, err := r.inputBindingPacket(ctx, answer)
	if err != nil || len(packet) != 1 || packet[0].Kind != assemble.Waiting {
		t.Fatalf("packet=%+v err=%v", packet, err)
	}
	q.ID = "second-question"
	q.Input = aged.ID
	// A question with no item is still an open offered question. It must prevent
	// an option answer from being falsely attributed to the other question.
	if err := waiting.NewConversationQuestions(r.db).Ask(ctx, q, 2); err != nil {
		t.Fatal(err)
	}
	ambiguous := admit("ambiguous", "2", now.Add(time.Hour+3*time.Second), surface)
	if d := read(ambiguous); !d.Unresolved {
		t.Fatal("two offered questions fired a rule", d)
	}
}

func TestInputBindingRollsBackAdmission(t *testing.T) {
	r := controlRuntime(t)
	ctx := t.Context()
	r.intakeReplay = func(context.Context, ledger.Event) error { return nil }
	if err := r.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER fail_binding BEFORE INSERT ON conv_event WHEN NEW.scaffold_kind='input_binding' BEGIN SELECT RAISE(ABORT,'fixture_binding_failure');END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, text := "rollback", "hello"
	if _, err := r.Input(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Input{InputID: &id, Text: &text}); err == nil {
		t.Fatal("binding failure accepted input")
	}
	if err := r.db.Write(ctx, func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conv_event WHERE json_extract(origin,'$.locator')=?`, id).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("orphan person input")
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM inbox WHERE native_id=?`, id).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("orphan receipt")
		}
		_, err := tx.ExecContext(ctx, "DROP TRIGGER fail_binding")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Input(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Input{InputID: &id, Text: &text}); err != nil {
		t.Fatal("same-id retry after rollback", err)
	}
}

func TestInputBindingNoticeAndScope(t *testing.T) {
	r := controlRuntime(t)
	ctx := t.Context()
	now := time.Now().UTC()
	state := intakereplay.State{Clock: now, Focus: intakereplay.Target{Class: "none"}, Items: []intakereplay.Item{{ID: "background", Title: "Inspect the garden", Kind: "run", State: "running", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(ctx, state, intakereplay.Input{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]map[string]string{{"Run": "background", "Person": r.person, "Agent": r.agent, "Conversation": r.conversation}})
	if _, err := r.journal().Admit(ctx, guard.EntryBackgroundNotice, ledger.Input{ID: "notice", ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "background_notice", Locator: "notice", OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", Body: string(raw)}); err != nil {
		t.Fatal(err)
	}
	input, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "reply-notice", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Surface: ledger.Surface{Kind: "tui", ReplyTo: "notice"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.journal().Get(ctx, inputBindingID(input.ID))
	if err != nil {
		t.Fatal(err)
	}
	var d binding.Decision
	if err = json.Unmarshal([]byte(saved.Body.Text()), &d); err != nil {
		t.Fatal(err)
	}
	if d.Target.ID != "background" || d.Decider != "reply-or-card" {
		t.Fatal(d)
	}
	foreign, err := r.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: "foreign-reply", PersonID: r.person, AgentID: r.agent, ConversationID: "another-conversation", Surface: ledger.Surface{Kind: "tui", ReplyTo: "notice"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err = r.journal().Get(ctx, inputBindingID(foreign.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(saved.Body.Text()), &d); err != nil {
		t.Fatal(err)
	}
	if d.Target.Class != "none" || !d.Unresolved {
		t.Fatal("foreign reply stole notice target", d)
	}
}

func TestInputBindingRecoveryDoesNotStealFocus(t *testing.T) {
	r := controlRuntime(t)
	ctx := t.Context()
	now := time.Now().UTC()
	state := intakereplay.State{Clock: now, Focus: intakereplay.Target{Class: "none"}, Items: []intakereplay.Item{{ID: "a", Title: "Garden", Kind: "project", State: "active", LastActivity: now}, {ID: "b", Title: "Library", Kind: "project", State: "active", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(ctx, state, intakereplay.Input{}); err != nil {
		t.Fatal(err)
	}
	makeInput := func(id, text string) ledger.Input {
		return ledger.Input{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text}
	}
	old, err := ledger.New(r.db, nil).Admit(ctx, guard.EntryOwner, makeInput("legacy", "Garden"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.journal().Admit(ctx, guard.EntryOwner, makeInput("recent", "Library")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.inputBindingPacket(ctx, old); err != nil {
		t.Fatal(err)
	}
	next, err := r.journal().Admit(ctx, guard.EntryOwner, makeInput("next", "continue"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.journal().Get(ctx, inputBindingID(next.ID))
	if err != nil {
		t.Fatal(err)
	}
	var d binding.Decision
	if err = json.Unmarshal([]byte(saved.Body.Text()), &d); err != nil {
		t.Fatal(err)
	}
	if d.Target.ID != "b" || !d.Unresolved {
		t.Fatal("recovered older input replaced recent focus", d)
	}
}
