// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

const messengerTestPerson = "person"

func messengerRecords(t *testing.T) (*canon.Store, *messenger.Store, messenger.Source, time.Time) {
	t.Helper()
	db := database(t)
	store := messenger.Open(db, nil, nil)
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: messengerTestPerson, Platform: "fixture", Account: "acct-1",
		Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	if err := store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	return db, store, source, start
}

func admitMessages(t *testing.T, store *messenger.Store, source messenger.Source, messages ...messenger.Message) {
	t.Helper()
	if _, err := store.AdmitPage(t.Context(), source, "dm", "", messenger.Page{Messages: messages, Next: messenger.Cursor("page")}); err != nil {
		t.Fatal(err)
	}
}

func messengerMessage(id string, route messenger.Route, at time.Time, text string) messenger.Message {
	return messenger.Message{ID: id, Route: route, At: at, Text: text, Author: messenger.Author{ID: "author-" + id, Name: "someone"}}
}

// messengerResolve reads the admitted event so the test resolves evidence from
// canon rather than trusting the proposed item.
func messengerResolve(db *canon.Store) ResolveEvidence {
	return func(ctx context.Context, tx *canon.Tx, ref Evidence) (Origin, error) {
		var id, agent, speaker, person, origin, body, occurred string
		if err := tx.QueryRowContext(ctx, `SELECT id,agent_id,speaker_kind,coalesce(person_id,''),origin,coalesce(body,''),occurred_at FROM conv_event WHERE id=?`, ref.ID).
			Scan(&id, &agent, &speaker, &person, &origin, &body, &occurred); err != nil {
			return Origin{}, err
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return Origin{}, err
		}
		return Origin{ID: id, Person: person, Agent: agent, Speaker: guard.SpeakerKind(speaker), Quote: body,
			OccurredAt: at.Unix(), Scope: Local, Messenger: strings.Contains(origin, `"kind":"messenger"`)}, nil
	}
}

func prepareMessenger(t *testing.T, db *canon.Store) MessengerBatch {
	t.Helper()
	batch, err := New(db).PrepareMessengerExtraction(t.Context(), messengerTestPerson, 32, 3, 2*time.Minute, messengerResolve(db))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

// The person's own records after the watermark form one utterance per burst.
// A participant's message is context only, and history before the connection
// watermark never becomes evidence or an extraction input.
func TestMessengerExtractionReadsOnlyPersonRecordsAfterWatermark(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("history", messenger.RoutePerson, start.Add(-time.Hour), "I lived in Busan."),
		messengerMessage("other", messenger.RouteParticipant, start.Add(time.Minute), "The person lives on Mars."),
		messengerMessage("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
		messengerMessage("mine-2", messenger.RoutePerson, start.Add(3*time.Minute), "Especially in the morning."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("utterances: %+v", batch.Utterances)
	}
	utterance := batch.Utterances[0]
	if utterance.Chat != "dm" || len(utterance.Events) != 2 {
		t.Fatalf("person evidence: %+v", utterance)
	}
	for _, event := range utterance.Events {
		if event.ID != "messenger:fixture:acct-1:dm:mine#1" && event.ID != "messenger:fixture:acct-1:dm:mine-2#1" {
			t.Fatalf("non-person event reached evidence: %+v", event)
		}
	}
	if len(utterance.Context) == 0 || !strings.Contains(utterance.Context[len(utterance.Context)-1].Text, "Mars") {
		t.Fatalf("missing untrusted context: %+v", utterance.Context)
	}
	items := []Item{{Kind: Preference, Content: "Prefers jasmine tea in the morning", Subject: Subject{Kind: Person, ID: messengerTestPerson},
		Basis: Stated, Evidence: utterance.Events}}
	if err := ValidateMessengerExtraction(batch, items); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return ApplyMessengerExtraction(t.Context(), tx, batch, items, messengerResolve(db))
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "agent-a", Person: messengerTestPerson}, Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 1 {
		t.Fatalf("recalled: %+v %v", hits, err)
	}
	item := hits[0]
	if item.Holder != All || item.SourceAgent != "" || len(item.Evidence) != 2 {
		t.Fatalf("messenger item shape: %+v", item)
	}
	for _, evidence := range item.Evidence {
		if evidence.SourceAgent != "" {
			t.Fatalf("evidence gained a source agent: %+v", evidence)
		}
	}
}

// A statement made by someone else in the same chat cannot become an item
// about the person, even when the model proposes one.
func TestMessengerExtractionRefusesOtherSpeakersAsPersonEvidence(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("other", messenger.RouteParticipant, start.Add(time.Minute), "The person lives on Mars."),
		messengerMessage("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("utterances: %+v", batch.Utterances)
	}
	var contextID string
	for _, message := range batch.Utterances[0].Context {
		if strings.Contains(message.Text, "Mars") {
			contextID = message.ID
		}
	}
	if contextID == "" {
		t.Fatal("participant record absent from context")
	}
	item := Item{Kind: Fact, Content: "Lives on Mars", Subject: Subject{Kind: Person, ID: messengerTestPerson},
		Basis: Stated, Evidence: []Evidence{{Kind: Event, ID: contextID, Precision: Exact}}}
	if err := ValidateMessengerExtraction(batch, []Item{item}); !errors.Is(err, ErrEvidence) && !errors.Is(err, ErrPersonEvidence) {
		t.Fatalf("participant evidence accepted: %v", err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return ApplyMessengerExtraction(t.Context(), tx, batch, []Item{item}, messengerResolve(db))
	}); err == nil {
		t.Fatal("apply accepted participant evidence about the person")
	}
}

// Repeated scans after the committed cursor make no further utterance, and a
// chat or source with learning off is never read. A never-leaves chat is
// skipped even when learning is on.
func TestMessengerExtractionSwitchesAndProgress(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("first scan: %+v", batch.Utterances)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return ApplyMessengerExtraction(t.Context(), tx, batch, nil, messengerResolve(db))
	}); err != nil {
		t.Fatal(err)
	}
	if next := prepareMessenger(t, db); len(next.Utterances) != 0 {
		t.Fatalf("committed records were read again: %+v", next.Utterances)
	}
	admitMessages(t, store, source,
		messengerMessage("second", messenger.RoutePerson, start.Add(4*time.Minute), "I enjoy hiking."),
	)
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", messenger.Switches{Sync: true, Learn: false, Embed: true}); err != nil {
		t.Fatal(err)
	}
	if next := prepareMessenger(t, db); len(next.Utterances) != 0 {
		t.Fatalf("chat learning switch ignored: %+v", next.Utterances)
	}
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", messenger.Switches{Sync: true, Learn: true, Embed: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatScope(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", "never-leaves"); err != nil {
		t.Fatal(err)
	}
	if next := prepareMessenger(t, db); len(next.Utterances) != 0 {
		t.Fatalf("never-leaves chat learned: %+v", next.Utterances)
	}
	if err := store.SetChatScope(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", "local"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, false); err != nil {
		t.Fatal(err)
	}
	if next := prepareMessenger(t, db); len(next.Utterances) != 0 {
		t.Fatalf("source learning switch ignored: %+v", next.Utterances)
	}
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	if next := prepareMessenger(t, db); len(next.Utterances) != 1 {
		t.Fatalf("learning did not resume: %+v", next.Utterances)
	}
}

// A message LINA sent in the person's name returns through sync as an agent
// record and never becomes the person's words.
func TestMessengerExtractionIgnoresOwnWrites(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	effect := messenger.Effect{ID: "effect-1", Platform: source.Platform, Account: source.Account, Chat: "dm",
		Agent: "agent-a", Verb: "send", ClientID: "client-1", Message: "sent-1", State: messenger.EffectPending}
	if err := store.RecordEffect(t.Context(), effect); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleEffect(t.Context(), effect.ID, messenger.EffectSent, "sent-1"); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("sent-1", messenger.RoutePerson, start.Add(time.Minute), "I will call you tomorrow."),
		messengerMessage("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 || len(batch.Utterances[0].Events) != 1 {
		t.Fatalf("own write treated as person speech: %+v", batch.Utterances)
	}
	if batch.Utterances[0].Events[0].ID != "messenger:fixture:acct-1:dm:mine#1" {
		t.Fatalf("wrong evidence: %+v", batch.Utterances[0].Events)
	}
}

func TestMessengerExtractionRejectsForeignChatAndScope(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("utterances: %+v", batch.Utterances)
	}
	item := Item{Kind: Preference, Content: "Prefers jasmine tea", Subject: Subject{Kind: Person, ID: messengerTestPerson},
		Basis: Stated, Evidence: batch.Utterances[0].Events}
	item.Holder = "agent-a"
	if err := ValidateMessengerExtraction(batch, []Item{item}); err == nil {
		t.Fatal("agent holder accepted for a messenger item")
	}
	item.Holder = ""
	item.Kind = SharedHistory
	if err := ValidateMessengerExtraction(batch, []Item{item}); err == nil {
		t.Fatal("agent-held kind accepted for a messenger item")
	}
	// A different source cursor makes the prepared batch obsolete.
	batch.Sources[0].After = 99
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return ApplyMessengerExtraction(t.Context(), tx, batch, []Item{item}, messengerResolve(db))
	}); err == nil {
		t.Fatal("stale source cursor applied")
	}
}

func TestMessengerExtractionNoRecordsMakesNoBatch(t *testing.T) {
	db, _, _, _ := messengerRecords(t)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 0 {
		t.Fatalf("empty source produced work: %+v", batch.Utterances)
	}
	for _, source := range batch.Sources {
		if source.After != source.Through {
			t.Fatalf("empty scan advanced a source: %+v", source)
		}
	}
}

// An import the person started with learning on moves the watermark back and
// resets progress, so older history becomes input once.
func TestMessengerExtractionImportWithLearning(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("history", messenger.RoutePerson, start.Add(-2*time.Hour), "I lived in Busan."),
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	if batch := prepareMessenger(t, db); len(batch.Utterances) != 1 {
		t.Fatalf("history before the watermark was read: %+v", batch.Utterances)
	}
	if err := store.SetLearningWatermark(t.Context(), messengerTestPerson, source.Platform, source.Account, start.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 2 || !strings.Contains(batch.Utterances[0].Text, "Busan") {
		t.Fatalf("imported history was not learned: %+v", batch.Utterances)
	}
	sources, err := store.Sources(t.Context(), messengerTestPerson)
	if err != nil || len(sources) != 1 || !sources[0].Learn {
		t.Fatalf("source learning switch: %+v %v", sources, err)
	}
}
