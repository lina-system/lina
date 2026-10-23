// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestEmotionCannotBeWrittenWithoutConfirmedEvidence(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := create(t, s, "")
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.Emotion = "unconfirmed"
	if err := s.SetState(t.Context(), a.ID, state, "person"); !errors.Is(err, agents.ErrInvalid) {
		t.Fatalf("emotion write without confirmed event accepted: %v", err)
	}
}

func TestEmotionLayersDecayAndStayWithinTheirAgentAndConversation(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	a, b := create(t, s, ""), create(t, s, "")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := agents.DefaultEmotionPolicy()
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := agents.EmotionEvent{ConversationID: "first", EventID: "confirmed", Affect: agents.Affect{Valence: 0.8, Arousal: 0.4}, At: now}
	changed, err := s.RecordEmotion(t.Context(), a.ID, agents.StateRevision(state), event, "person", policy)
	if err != nil || !changed.Recorded {
		t.Fatalf("event not recorded: %v %v", changed, err)
	}
	state, err = s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agents.ProjectEmotion(state, "first", a.Revision, now, policy, false)
	if err != nil || view.Short != event.Affect || math.Abs(view.Mood.Valence-0.2) > 1e-9 || view.Disposition != (agents.Affect{}) {
		t.Fatalf("layers: %+v %v", view, err)
	}
	other, err := agents.ProjectEmotion(state, "second", a.Revision, now, policy, false)
	if err != nil || other.Short != (agents.Affect{}) || other.Mood != view.Mood {
		t.Fatalf("conversation leak: %+v %v", other, err)
	}
	later, err := agents.ProjectEmotion(state, "first", a.Revision, now.Add(72*time.Hour), policy, false)
	if err != nil || later.Short != (agents.Affect{}) || math.Abs(later.Mood.Valence-0.1) > 1e-9 {
		t.Fatalf("clock decay: %+v %v", later, err)
	}
	baseline, err := agents.ProjectEmotion(state, "first", a.Revision, now.Add(100*policy.MoodHalfLife), policy, false)
	if err != nil || math.Abs(baseline.Mood.Valence) > 1e-9 {
		t.Fatalf("did not return to baseline: %+v %v", baseline, err)
	}
	if changed, err := s.RecordEmotion(t.Context(), a.ID, agents.StateRevision(state), event, "person", policy); err != nil || changed.Recorded {
		t.Fatalf("event counted twice: %v %v", changed, err)
	}
	current, err := s.Get(t.Context(), a.ID)
	if err != nil || current.Revision != a.Revision {
		t.Fatal("short emotion revised fixed persona")
	}
	untouched, err := s.State(t.Context(), b.ID)
	if err != nil || untouched.Affect != nil {
		t.Fatalf("other agent changed: %+v %v", untouched, err)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.State(t.Context(), a.ID)
	if err != nil || !reflect.DeepEqual(persisted, state) {
		t.Fatalf("emotion not durable: %+v %v", persisted, err)
	}
}

func TestEmotionSuppressorHidesStateWithoutErasingIt(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := create(t, s, "")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := agents.DefaultEmotionPolicy()
	state, err := s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordEmotion(t.Context(), a.ID, agents.StateRevision(state), agents.EmotionEvent{ConversationID: "chat", EventID: "confirmed", Affect: agents.Affect{Valence: 1, Arousal: 1}, At: now}, "person", policy)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	sensitive, err := agents.ProjectEmotion(state, "chat", a.Revision, now, policy, true)
	if err != nil || !sensitive.Suppressed || sensitive.Short != (agents.Affect{}) || sensitive.Mood != (agents.Affect{}) {
		t.Fatalf("sensitive expression leaked: %+v %v", sensitive, err)
	}
	_, err = s.SetEmotionExpression(t.Context(), a.ID, agents.ExpressionRevision(state), false, "off-request", "person")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := s.State(t.Context(), a.ID)
	if err != nil || !reflect.DeepEqual(disabled.Affect, state.Affect) {
		t.Fatal("turning expression off erased emotion")
	}
	view, err := agents.ProjectEmotion(disabled, "chat", a.Revision, now, policy, false)
	if err != nil || !view.Suppressed || view.Short != (agents.Affect{}) || view.Mood != (agents.Affect{}) {
		t.Fatalf("disabled expression leaked: %+v %v", view, err)
	}
}

func TestSensitiveShortEmotionExpiresOnTheClock(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	p := agents.DefaultEmotionPolicy()
	state := agents.State{Affect: &agents.EmotionRecord{Short: map[string]agents.ShortEmotion{"chat": {At: now, Sensitive: true}}}}
	before, err := agents.ProjectEmotion(state, "chat", 1, now, p, false)
	if err != nil || !before.Suppressed {
		t.Fatalf("sensitive short: %+v %v", before, err)
	}
	after, err := agents.ProjectEmotion(state, "chat", 1, now.Add(p.ShortLifetime), p, false)
	if err != nil || after.Suppressed {
		t.Fatalf("expired short still suppresses: %+v %v", after, err)
	}
}
