// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestWorkMethodFillSurvivesPresetRestartAndRetirement(t *testing.T) {
	s, db, root := setup(t)
	p := agents.Preset{ID: "lina", Core: "core", DefaultVoice: "voice", WorkMethod: `{"schema":1,"preset":"lina","ask":{"form":"choices"},"fill":["ask.form"]}`}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	a, b := create(t, s, "lina"), create(t, s, "lina")
	pin, err := s.Prepare(t.Context(), "pinned", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	fill := func(expected, key, value, source string) (bool, error) {
		return s.FillWorkMethod(t.Context(), a.ID, pin.Revision, expected, key, json.RawMessage(value), source, "person")
	}
	original := agents.StateRevision(state)
	for _, tc := range []struct{ key, value, source string }{{"ask.form", `"narrowed"`, ""}, {"ask.per_turn", `2`, "confirmed"}, {"ask.form", `"open"`, "confirmed"}} {
		if changed, err := fill(original, tc.key, tc.value, tc.source); err == nil || changed {
			t.Fatal("invalid fill accepted", tc)
		}
	}
	if changed, err := fill(original, "ask.form", `"narrowed"`, "confirmed"); err != nil || !changed {
		t.Fatal("fill failed", err)
	}
	state, _ = s.State(t.Context(), a.ID)
	if changed, err := fill(agents.StateRevision(state), "ask.form", `"narrowed"`, "later"); err != nil || changed {
		t.Fatal("repeat fill changed evidence", err)
	}
	if _, err := fill(original, "ask.form", `"narrowed"`, "later"); !errors.Is(err, agents.ErrConflict) {
		t.Fatal("stale state accepted", err)
	}
	if _, err := fill(agents.StateRevision(state), "ask.form", `"choices"`, "later"); !errors.Is(err, agents.ErrConflict) {
		t.Fatal("conflicting fill accepted", err)
	}
	// Change all three preset members together. The old turn and fill must survive.
	p.Core, p.DefaultVoice = "next core", "next voice"
	p.WorkMethod = `{"schema":1,"preset":"lina","ask":{"form":"choices","per_turn":2},"fill":["ask.form"]}`
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	if _, err := fill(agents.StateRevision(state), "ask.form", `"narrowed"`, "later"); !errors.Is(err, agents.ErrConflict) {
		t.Fatal("stale persona accepted", err)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	old, err := reopened.Prepared(t.Context(), "pinned", a.ID)
	if err != nil || old.WorkMethod != pin.WorkMethod || old.WorkMethodRevision != pin.WorkMethodRevision || old.Commit != pin.Commit {
		t.Fatal("pin lost across reopen", err)
	}
	next, err := reopened.Prepare(t.Context(), "next", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = reopened.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection := agents.ProjectWorkMethod(next, state)
	if string(projection.Values["ask.form"]) != `"narrowed"` || projection.Filled["ask.form"].Source != "confirmed" || projection.Filled["ask.form"].Retired {
		t.Fatal("fill lost", projection)
	}
	other, err := reopened.Prepare(t.Context(), "other", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherState, _ := reopened.State(t.Context(), b.ID)
	if projected := agents.ProjectWorkMethod(other, otherState); len(projected.Filled) != 0 || string(projected.Values["ask.form"]) != `"choices"` {
		t.Fatal("fill crossed agents")
	}
	// A voice change keeps the record even though it mints another persona commit.
	layers := next.Layers
	layers.UserVoice = "personal"
	if _, err := reopened.Revise(t.Context(), a.ID, next.Revision, layers, "person"); err != nil {
		t.Fatal(err)
	}
	voice, err := reopened.Prepare(t.Context(), "voice", a.ID)
	if err != nil || voice.WorkMethodRevision != next.WorkMethodRevision {
		t.Fatal("voice edit lost record", err)
	}
	p.WorkMethod = `{"schema":1,"preset":"lina","ask":{"form":"choices"}}`
	if err := reopened.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	retired, err := reopened.Prepare(t.Context(), "retired", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, _ = reopened.State(t.Context(), a.ID)
	projection = agents.ProjectWorkMethod(retired, state)
	if !projection.Filled["ask.form"].Retired || projection.Filled["ask.form"].Source != "confirmed" || string(projection.Values["ask.form"]) != `"choices"` {
		t.Fatal("retired value applied or erased", projection)
	}
	if changed, err := reopened.FillWorkMethod(t.Context(), a.ID, retired.Revision, agents.StateRevision(state), "ask.form", json.RawMessage(`"narrowed"`), "later", "person"); err == nil || changed {
		t.Fatal("retired key fill accepted")
	}
	p.WorkMethod = `{"schema":1,"preset":"lina","fill":["ask.form"]}`
	if err := reopened.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	restored, _ := reopened.Prepare(t.Context(), "restored", a.ID)
	if got := agents.ProjectWorkMethod(restored, state); got.Filled["ask.form"].Retired || string(got.Values["ask.form"]) != `"narrowed"` {
		t.Fatal("stored fill did not reactivate", got)
	}
	// Close SQLite as well as both agent handles, then read only durable bytes.
	reopened.Close()
	s.Close()
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, err := canon.OpenReader(t.Context(), filepath.Join(root, "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(t.Context())
	restarted, err := agents.Open(root, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	durableState, err := restarted.State(t.Context(), a.ID)
	if err != nil || durableState.WorkMethodFills["ask.form"].Source != "confirmed" {
		t.Fatal("fill did not survive database restart", err)
	}
	durablePin, err := restarted.Prepared(t.Context(), "pinned", a.ID)
	if err != nil || durablePin.WorkMethodRevision != pin.WorkMethodRevision || durablePin.WorkMethod != pin.WorkMethod {
		t.Fatal("record pin did not survive database restart", err)
	}
}

func TestWorkMethodTriplePublicationRollsBackAndDeduplicates(t *testing.T) {
	s, db, _ := setup(t)
	p := agents.Preset{ID: "dev", Core: "one", DefaultVoice: "voice one", WorkMethod: `{"schema":1,"preset":"dev"}`}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	a, b := create(t, s, "dev"), create(t, s, "dev")
	old, err := s.Prepare(t.Context(), "old", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER refuse_method BEFORE INSERT ON agent_revisions WHEN (SELECT count(*) FROM agent_revisions WHERE revision=2)>0 BEGIN SELECT RAISE(ABORT,'fixture'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.Core, p.DefaultVoice, p.WorkMethod = "two", "voice two", `{"schema":1,"preset":"dev","workers":{"dispatch":true}}`
	if err := s.UpdatePreset(t.Context(), p, "person"); err == nil {
		t.Fatal("partial publication accepted")
	}
	fresh := create(t, s, "dev")
	for _, id := range []string{a.ID, b.ID, fresh.ID} {
		got, err := s.Prepare(t.Context(), "failed:"+id, id)
		if err != nil || got.Layers != old.Layers || got.WorkMethod != old.WorkMethod || got.Revision != 1 {
			t.Fatal("partial triple escaped rollback", got, err)
		}
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DROP TRIGGER refuse_method")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	next, err := s.Prepare(t.Context(), "published", a.ID)
	if err != nil || next.Revision != 2 || next.Layers.Core != p.Core || next.Layers.DefaultVoice != p.DefaultVoice || next.WorkMethodRevision == old.WorkMethodRevision {
		t.Fatal("triple incomplete", next, err)
	}
	p.WorkMethod = " { \"workers\": {\"dispatch\": true}, \"preset\": \"dev\", \"schema\": 1 } "
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	same, err := s.Prepare(t.Context(), "same", a.ID)
	if err != nil || same.Commit != next.Commit || same.Revision != next.Revision {
		t.Fatal("identical publication minted revision", err)
	}
	independent := create(t, s, "")
	empty, err := s.Prepare(t.Context(), "description", independent.ID)
	if err != nil || empty.WorkMethod != `{"schema":1}` {
		t.Fatal("description record is not empty", err)
	}
}
