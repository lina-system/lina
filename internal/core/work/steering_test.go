// SPDX-License-Identifier: Apache-2.0
package work

import (
	"github.com/thisisjun786/lina/internal/core/canon"
	"testing"
	"time"
)

func TestSteeringMailboxReceiptsAndResume(t *testing.T) {
	t.Parallel()
	for _, tier := range []string{"push", "turn", "pull"} {
		t.Run(tier, func(t *testing.T) {
			s, db := testStore(t)
			sent := time.Unix(100, 0).UTC()
			m := SteeringMessage{ID: "message", Session: "fake:session", TaskID: "task", Person: "person", Agent: "agent", Conversation: "conversation", Text: "write result", Tier: tier, Mechanism: "fixture", SentAt: sent}
			if _, err := s.StoreSteering(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StoreSteering(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			if err := s.SteeringState(t.Context(), m.Session, m.ID, "held", "session_down", sent); err != nil {
				t.Fatal(err)
			}
			resumed := New(db)
			pending, err := resumed.PendingSteering(t.Context(), m.Session)
			if err != nil || len(pending) != 1 {
				t.Fatalf("mail missing after resume: %+v %v", pending, err)
			}
			if err = resumed.SteeringState(t.Context(), m.Session, m.ID, "held", "delivery_uncertain", sent.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			pending, err = New(db).PendingSteering(t.Context(), m.Session)
			if err != nil || len(pending) != 0 {
				t.Fatal("claimed delivery was replayed", pending, err)
			}
			if err = resumed.SteeringState(t.Context(), m.Session, m.ID, "delivered", "", sent.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err = resumed.AcknowledgeSteering(t.Context(), m.Session, []string{m.ID}, []string{m.ID}, sent.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			messages, err := resumed.SteeringMessages(t.Context(), m.Session)
			if err != nil || len(messages) != 1 || messages[0].State != "read" || messages[0].ActionLatencyMS == nil || *messages[0].ActionLatencyMS != 2000 {
				t.Fatalf("receipt: %+v %v", messages, err)
			}
			receipts, err := resumed.SteeringReceipts(t.Context(), m.ID)
			if err != nil || len(receipts) != 5 {
				t.Fatalf("ledger: %+v %v", receipts, err)
			}
			if err = resumed.AcknowledgeSteering(t.Context(), "foreign", []string{m.ID}, nil, sent); err == nil {
				t.Fatal("foreign read accepted")
			}
			m.Text = "conflicting request"
			if _, err = resumed.StoreSteering(t.Context(), m); err == nil {
				t.Fatal("conflicting id accepted")
			}
		})
	}
}

func TestSteeringReceiptsProjectIntoTaskLedger(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	startFixture(t, s)
	m := SteeringMessage{ID: "mail", Session: "fake:thread-1", TaskID: "task-1", Person: "p", Agent: "a", Conversation: "c", Text: "amend result", Tier: "turn", SentAt: time.Now().UTC()}
	if _, err := s.StoreSteering(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := s.SteeringState(t.Context(), m.Session, m.ID, "delivered", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	card, err := s.Get(t.Context(), m.TaskID)
	if err != nil || len(card.Steering) != 1 || card.Steering[0].State != "delivered" {
		t.Fatal(card, err)
	}
	events, err := s.Ledger(t.Context(), m.TaskID)
	if err != nil || events[len(events)-1].Kind != "steering" {
		t.Fatal(events, err)
	}
}

func TestSteeringRestoreAndInvalidAcknowledgement(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	m := SteeringMessage{ID: "mail", Session: "fake:thread", Person: "p", Agent: "a", Conversation: "c", Text: "amend result", Tier: "pull", SentAt: time.Now().UTC()}
	if _, err := s.StoreSteering(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeSteering(t.Context(), m.Session, []string{m.ID}, nil, time.Now()); err == nil {
		t.Fatal("unseen message marked read")
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return FenceRestoredSteering(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingSteering(t.Context(), m.Session)
	if err != nil || len(pending) != 0 {
		t.Fatal("restored mail gained authority", pending, err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM work_steering_receipts")
		return err
	}); err == nil {
		t.Fatal("receipt history was mutable")
	}
}

func TestSteeringSourceSurvivesResumeAndCannotBeReattributed(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	m := SteeringMessage{ID: "source", Session: "fake:thread", Person: "owner", Agent: "manager", Conversation: "work", Text: "continue", Tier: "turn", SentAt: time.Now().UTC(), Sender: "manager", SpeakerKind: "agent"}
	if _, err := s.StoreSteering(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	resumed := New(db)
	messages, err := resumed.SteeringMessages(t.Context(), m.Session)
	if err != nil || len(messages) != 1 || messages[0].Sender != "manager" || messages[0].SpeakerKind != "agent" {
		t.Fatal(messages, err)
	}
	m.Sender, m.SpeakerKind = "owner", "person"
	if _, err := resumed.StoreSteering(t.Context(), m); err == nil {
		t.Fatal("retry changed manager input into person input")
	}
}
