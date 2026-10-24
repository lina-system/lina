// SPDX-License-Identifier: Apache-2.0
package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/retract"
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
func messengerResolve(db *canon.Store) memory.ResolveEvidence {
	return func(ctx context.Context, tx *canon.Tx, ref memory.Evidence) (memory.Origin, error) {
		var id, agent, speaker, person, origin, body, occurred string
		if err := tx.QueryRowContext(ctx, `SELECT id,agent_id,speaker_kind,coalesce(person_id,''),origin,coalesce(body,''),occurred_at FROM conv_event WHERE id=?`, ref.ID).
			Scan(&id, &agent, &speaker, &person, &origin, &body, &occurred); err != nil {
			return memory.Origin{}, err
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return memory.Origin{}, err
		}
		return memory.Origin{ID: id, Person: person, Agent: agent, Speaker: guard.SpeakerKind(speaker), Quote: body,
			OccurredAt: at.Unix(), Scope: memory.Local, Messenger: strings.Contains(origin, `"kind":"messenger"`)}, nil
	}
}

func prepareMessenger(t *testing.T, db *canon.Store) memory.MessengerBatch {
	t.Helper()
	batch, err := memory.New(db).PrepareMessengerExtraction(t.Context(), messengerTestPerson, 32, 3, 2*time.Minute, messengerResolve(db))
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
	items := []memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea in the morning", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: utterance.Events}}
	if err := memory.ValidateMessengerExtraction(batch, items); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, items, messengerResolve(db))
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "agent-a", Person: messengerTestPerson}, memory.Query{At: time.Now().Unix()})
	if err != nil || len(hits) != 1 {
		t.Fatalf("recalled: %+v %v", hits, err)
	}
	item := hits[0]
	if item.Holder != memory.All || item.SourceAgent != "" || len(item.Evidence) != 2 {
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
	item := memory.Item{Kind: memory.Fact, Content: "Lives on Mars", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: contextID, Precision: memory.Exact}}}
	if err := memory.ValidateMessengerExtraction(batch, []memory.Item{item}); !errors.Is(err, memory.ErrEvidence) && !errors.Is(err, memory.ErrPersonEvidence) {
		t.Fatalf("participant evidence accepted: %v", err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, []memory.Item{item}, messengerResolve(db))
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
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, nil, messengerResolve(db))
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
	item := memory.Item{Kind: memory.Preference, Content: "Prefers jasmine tea", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: batch.Utterances[0].Events}
	item.Holder = "agent-a"
	if err := memory.ValidateMessengerExtraction(batch, []memory.Item{item}); err == nil {
		t.Fatal("agent holder accepted for a messenger item")
	}
	item.Holder = ""
	item.Kind = memory.SharedHistory
	if err := memory.ValidateMessengerExtraction(batch, []memory.Item{item}); err == nil {
		t.Fatal("agent-held kind accepted for a messenger item")
	}
	// A different source cursor makes the prepared batch obsolete.
	batch.Sources[0].After = 99
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, []memory.Item{item}, messengerResolve(db))
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

// An import the person started with learning off states its newest record as
// the floor. That record and anything sharing its time stay outside the learned
// range, so the opt-out is not undone by the floor's own comparison.
func TestMessengerExtractionImportWithLearningOff(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("history", messenger.RoutePerson, start.Add(-2*time.Hour), "I lived in Busan."),
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	if err := store.SetLearningWatermark(t.Context(), messengerTestPerson, source.Platform, source.Account, start.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if batch := prepareMessenger(t, db); len(batch.Utterances) != 0 {
		t.Fatalf("learning-disabled import was read: %+v", batch.Utterances)
	}
}

// A source with learning off only advances its own cursor. It must not hold
// back the batch another source prepared, or one switch would stop learning
// everywhere.
func TestMessengerExtractionDisabledSourceKeepsOthersLearning(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	second := messenger.Source{Person: messengerTestPerson, Platform: "fixture", Account: "acct-2",
		Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	if err := store.Connect(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(t.Context(), second, []messenger.Chat{{ID: "dm", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, second.Platform, second.Account, false); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	admitMessages(t, store, second,
		messengerMessage("off", messenger.RoutePerson, start.Add(time.Minute), "I live on Mars."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 || len(batch.Sources) != 2 {
		t.Fatalf("batch: %+v", batch)
	}
	items := []memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: batch.Utterances[0].Events}}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, items, messengerResolve(db))
	}); err != nil {
		t.Fatalf("disabled source blocked the batch: %v", err)
	}
}

// A chat that became more private after preparation rebuilds its batch instead
// of writing the scope it was prepared under.
func TestMessengerExtractionRebuildsOnChatScopeChange(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("utterances: %+v", batch.Utterances)
	}
	if err := store.SetChatScope(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", "private"); err != nil {
		t.Fatal(err)
	}
	items := []memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: batch.Utterances[0].Events}}
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, items, messengerResolve(db))
	})
	if !errors.Is(err, memory.ErrJobConflict) {
		t.Fatalf("scope change applied: %v", err)
	}
}

// An edit that arrives after preparation replaces the record. The prepared
// event is no longer the live record, so the batch rebuilds from the edit
// instead of learning the superseded text.
func TestMessengerExtractionRebuildsOnEditedRecord(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("utterances: %+v", batch.Utterances)
	}
	edited := messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer green tea.")
	edited.Edited = start.Add(2 * time.Minute)
	admitMessages(t, store, source, edited)
	items := []memory.Item{{Kind: memory.Preference, Content: "Prefers jasmine tea", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: batch.Utterances[0].Events}}
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, items, messengerResolve(db))
	})
	if !errors.Is(err, memory.ErrJobConflict) {
		t.Fatalf("edited record applied: %v", err)
	}
}

// Turning a source's learning off states the excluded interval in the same
// transaction, so a record that arrives while it is off is not learned when the
// person turns it back on before the next scan.
func TestMessengerExtractionSourceSwitchExcludesItsInterval(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, false); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("off", messenger.RoutePerson, start.Add(time.Minute), "I live on Mars."),
	)
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	if batch := prepareMessenger(t, db); len(batch.Utterances) != 0 {
		t.Fatalf("record from the disabled interval was read: %+v", batch.Utterances)
	}
}

// The chat switch states the same interval for that chat, so re-enabling it
// does not learn the window it covered.
func TestMessengerExtractionChatSwitchExcludesItsInterval(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	off := messenger.Switches{Sync: true, Learn: false, Embed: true}
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", off); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("off", messenger.RoutePerson, start.Add(time.Minute), "I live on Mars."),
	)
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm",
		messenger.Switches{Sync: true, Learn: true, Embed: true}); err != nil {
		t.Fatal(err)
	}
	if batch := prepareMessenger(t, db); len(batch.Utterances) != 0 {
		t.Fatalf("record from the disabled interval was read: %+v", batch.Utterances)
	}
}

// A chat whose learning switch is off stays excluded when its scope leaves
// never-leaves: the two controls share one interval, and only their combined
// answer closes it.
func TestMessengerExtractionChatScopeKeepsItsIntervalOpen(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("on", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm",
		messenger.Switches{Sync: true, Learn: false, Embed: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatScope(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", messenger.ChatNeverLeaves); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatScope(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", messenger.ChatLocal); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("off", messenger.RoutePerson, start.Add(2*time.Minute), "I live on Mars."),
	)
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm",
		messenger.Switches{Sync: true, Learn: true, Embed: true}); err != nil {
		t.Fatal(err)
	}
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 || !strings.Contains(batch.Utterances[0].Text, "jasmine tea") ||
		strings.Contains(batch.Utterances[0].Text, "Mars") {
		t.Fatalf("record from the disabled interval was read: %+v", batch.Utterances)
	}
}

// An interval that covers no record is dropped when the switch returns, so a
// source that never admitted a record while it was off learns again.
func TestMessengerExtractionEmptyIntervalDoesNotBlockLaterRecords(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("later", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	if batch := prepareMessenger(t, db); len(batch.Utterances) != 1 {
		t.Fatalf("an empty interval excluded a later record: %+v", batch.Utterances)
	}
}

// A Discord bot-mode source stays searchable but does not feed learning until
// the owner confirms the platform's developer-policy reading; a personal source
// on the same platform learns normally.
func TestMessengerExtractionGatesBotModeDiscord(t *testing.T) {
	db := database(t)
	store := messenger.Open(db, nil, nil)
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	bot := messenger.Source{Person: messengerTestPerson, Platform: "discord", Account: "111", Mode: messenger.ModeOfficial,
		Adapter: "discord", State: messenger.StateConnected, ConnectedAt: start}
	personal := messenger.Source{Person: messengerTestPerson, Platform: "discord", Account: "222", Mode: messenger.ModeUnofficial,
		Adapter: "discord-personal", State: messenger.StateConnected, ConnectedAt: start,
		AcceptedTerms: &messenger.Terms{Person: messengerTestPerson, At: start, Text: "personal account terms"}}
	for _, source := range []messenger.Source{bot, personal} {
		if err := store.Connect(t.Context(), source); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm", Name: "Direct", Kind: "direct"}}); err != nil {
			t.Fatal(err)
		}
	}
	admitMessages(t, store, bot,
		messengerMessage("bot", messenger.RoutePerson, start.Add(time.Minute), "I live on Mars."),
	)
	admitMessages(t, store, personal,
		messengerMessage("personal", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 || !strings.Contains(batch.Utterances[0].Text, "jasmine tea") {
		t.Fatalf("bot-mode records fed learning: %+v", batch.Utterances)
	}
	var records int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM messenger_record WHERE account='111'").Scan(&records)
	}); err != nil || records != 1 {
		t.Fatalf("bot-mode record was not searchable: %d %v", records, err)
	}
}

// Deleting every record while learning is off and turning it back on leaves no
// open interval: the position counts deleted records too, so a later message is
// still learned.
func TestMessengerExtractionDeletionDoesNotKeepIntervalOpen(t *testing.T) {
	db, _, source, start := messengerRecords(t)
	removing := messenger.Open(db, nil, noRemovals{})
	if err := removing.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, false); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, removing, source,
		messengerMessage("gone", messenger.RoutePerson, start.Add(time.Minute), "I live on Mars."),
	)
	if _, err := removing.DeleteRecords(t.Context(), source, "dm", []string{"gone"}); err != nil {
		t.Fatal(err)
	}
	if err := removing.SetSourceLearn(t.Context(), messengerTestPerson, source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, removing, source,
		messengerMessage("later", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
	)
	if batch := prepareMessenger(t, db); len(batch.Utterances) != 1 {
		t.Fatalf("a deleted interval excluded a later record: %+v", batch.Utterances)
	}
}

// A participant's turn breaks the burst even inside the window, so two answers
// to different questions are separate utterances and the second one carries the
// question it answers as context.
func TestMessengerExtractionBreaksBurstOnParticipantTurn(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("mine-1", messenger.RoutePerson, start.Add(time.Minute), "Yes."),
		messengerMessage("other", messenger.RouteParticipant, start.Add(time.Minute+10*time.Second), "Do you prefer tea or coffee?"),
		messengerMessage("mine-2", messenger.RoutePerson, start.Add(time.Minute+20*time.Second), "Tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 2 {
		t.Fatalf("participant turn did not break the burst: %+v", batch.Utterances)
	}
	if batch.Utterances[0].Text != "Yes." || batch.Utterances[1].Text != "Tea." {
		t.Fatalf("utterances: %+v", batch.Utterances)
	}
	context := batch.Utterances[1].Context
	if len(context) == 0 || !strings.Contains(context[len(context)-1].Text, "tea or coffee") {
		t.Fatalf("second utterance lost its question: %+v", context)
	}
}

// A context message that a memory restricts after preparation is not sent to
// the model: the batch rebuilds instead.
func TestMessengerExtractionRebuildsWhenContextBecomesRestricted(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("other", messenger.RouteParticipant, start.Add(time.Minute), "The person lives on Mars."),
		messengerMessage("mine", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 || len(batch.Utterances[0].Context) == 0 {
		t.Fatalf("prepared: %+v", batch.Utterances)
	}
	context := batch.Utterances[0].Context
	restrictEvent(t, db, context[len(context)-1].ID)
	if err := memory.New(db).CheckMessengerExtraction(t.Context(), batch, messengerResolve(db)); !errors.Is(err, memory.ErrJobConflict) {
		t.Fatalf("restricted context was accepted: %v", err)
	}
}

// A proposal that Apply would refuse is refused before the model result is
// cached, so one malformed item cannot stall learning.
func TestMessengerExtractionValidationRejectsUnwritableItems(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	admitMessages(t, store, source,
		messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer jasmine tea."),
	)
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("prepared: %+v", batch.Utterances)
	}
	evidence := batch.Utterances[0].Events
	valid := memory.Item{Kind: memory.Preference, Content: "Prefers jasmine tea", Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson},
		Basis: memory.Stated, Evidence: evidence}
	if err := memory.ValidateMessengerExtraction(batch, []memory.Item{valid}); err != nil {
		t.Fatalf("valid proposal refused: %v", err)
	}
	for name, mutate := range map[string]func(*memory.Item){
		"empty content":   func(i *memory.Item) { i.Content = "" },
		"revision":        func(i *memory.Item) { i.Revision = 2 },
		"validity":        func(i *memory.Item) { i.ValidFrom, i.ValidUntil = 10, 5 },
		"never-leaves":    func(i *memory.Item) { i.Scope = memory.NeverLeaves },
		"agent-held kind": func(i *memory.Item) { i.Kind = memory.SharedHistory },
		"source agent":    func(i *memory.Item) { i.SourceAgent = "agent-a" },
		"other subject":   func(i *memory.Item) { i.Subject = memory.Subject{Kind: memory.Person, ID: "someone"} },
	} {
		item := valid
		mutate(&item)
		if err := memory.ValidateMessengerExtraction(batch, []memory.Item{item}); err == nil {
			t.Fatalf("%s was accepted", name)
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
	if err := store.SetLearningWatermark(t.Context(), messengerTestPerson, source.Platform, source.Account, start.Add(-2*time.Hour), false); err != nil {
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

// noRemovals accepts deletions without hiding the retracted target, so a test
// can observe the record's own deleted state.
type noRemovals struct{}

func (noRemovals) Forget(context.Context, retract.Request) error { return nil }

// restrictEvent writes a never-leaves memory that cites one event, as a
// restriction the person's own scope choice produces.
func restrictEvent(t *testing.T, db *canon.Store, id string) {
	t.Helper()
	item := memory.Item{ID: "restricted", Kind: memory.Fact, Content: "Restricted statement", Holder: memory.All, Scope: memory.NeverLeaves,
		Subject: memory.Subject{Kind: memory.Person, ID: messengerTestPerson}, Basis: memory.Stated, State: memory.Active, Revision: 1,
		Evidence: []memory.Evidence{{Kind: memory.Event, ID: id, Precision: memory.Exact}}}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
VALUES(?,?,?,?,?,?,?,?,?,0,0,'',0,?)`, item.ID, messengerTestPerson, string(item.Holder), string(item.Subject.Kind), item.Subject.ID,
			"restricted statement", item.Content, string(item.Scope), string(item.State), raw); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO memory_revision(item_id,revision,record) VALUES(?,?,?)", item.ID, item.Revision, raw); err != nil {
			return err
		}
		evidence, err := json.Marshal(item.Evidence)
		if err != nil {
			return err
		}
		return canon.WriteEvidenceCitations(t.Context(), tx, "memory", item.ID, item.Revision, evidence)
	}); err != nil {
		t.Fatal(err)
	}
}

func database(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: c, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

// An old historical record can be admitted after a newer disabled record.
// Migration must not infer ledger ordering from the message timestamps.
func TestMessengerExtractionUpgradeExcludesOutOfOrderHistory(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", messenger.Switches{Sync: true}); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source,
		messengerMessage("disabled", messenger.RoutePerson, start.Add(time.Minute), "I live on Mars."),
		messengerMessage("backfill", messenger.RoutePerson, start.Add(-time.Hour), "Old history."))
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DELETE FROM messenger_learning_skip"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), canon.MessengerLearningSeed)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSwitches(t.Context(), messengerTestPerson, source.Platform, source.Account, "dm", messenger.Switches{Sync: true, Learn: true}); err != nil {
		t.Fatal(err)
	}
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 0 {
		t.Fatalf("upgrade leaked %d disabled utterances", len(batch.Utterances))
	}
	admitMessages(t, store, source, messengerMessage("enabled", messenger.RoutePerson, start.Add(2*time.Minute), "I prefer tea."))
	batch = prepareMessenger(t, db)
	if len(batch.Utterances) != 1 || batch.Utterances[0].Text != "I prefer tea." {
		t.Fatalf("enabled window: %+v", batch.Utterances)
	}
}

func TestMessengerExtractionRechecksSourcePolicy(t *testing.T) {
	db, store, source, start := messengerRecords(t)
	source.Platform, source.Mode = "discord", messenger.ModeUnofficial
	source.AcceptedTerms = &messenger.Terms{Person: messengerTestPerson, At: start, Text: "synthetic terms"}
	if err := store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	admitMessages(t, store, source, messengerMessage("mine", messenger.RoutePerson, start.Add(time.Minute), "I prefer tea."))
	batch := prepareMessenger(t, db)
	if len(batch.Utterances) != 1 {
		t.Fatalf("utterances=%d", len(batch.Utterances))
	}
	source.Mode = messenger.ModeOfficial
	if err := store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := memory.New(db).CheckMessengerExtraction(t.Context(), batch, messengerResolve(db)); !errors.Is(err, memory.ErrJobConflict) {
		t.Fatalf("gated source dispatched: %v", err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.ApplyMessengerExtraction(t.Context(), tx, batch, nil, messengerResolve(db))
	}); !errors.Is(err, memory.ErrJobConflict) {
		t.Fatalf("gated source applied: %v", err)
	}
}
