// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func assertScheduledWithdrawal(t *testing.T, h *harness, question protocol.Question, approval protocol.ApprovalRequest) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var raw []byte
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT record FROM authority_approvals WHERE json_extract(record, '$.Arguments.itemId')='withdrawn'`).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var record authority.Approval
	if err := json.Unmarshal(raw, &record); err != nil || record.State != authority.Stopped {
		t.Fatalf("scheduled worker withdrawal left its approval pending: state=%s err=%v", record.State, err)
	}
	if record.Conversation != question.ConversationID || approval.ConversationID == nil || *approval.ConversationID != question.ConversationID || record.TaskID == "" {
		t.Fatal("withdrawn request lost task or originating conversation")
	}
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "answer-fixture", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	value := "must not reach the worker"
	values := protocol.FormValues{"withdrawn": {String: &value}}
	for range 2 {
		id, err := client.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &question.Card.ID, Revision: &question.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values})
		if err != nil {
			t.Fatal(err)
		}
		frame, err := client.Receive(h.ctx)
		if err != nil || frame.Error != nil || frame.ReplyTo == nil || *frame.ReplyTo != id {
			t.Fatalf("withdrawn question answer: %+v %v", frame, err)
		}
		answer, ok := frame.Payload.(protocol.QuestionAnswer)
		if !ok || answer.Outcome == nil || *answer.Outcome != protocol.DecisionOutcomeStopped {
			t.Fatalf("withdrawn question accepted: %+v", frame)
		}
		id, err = client.AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &approval.Card.ID, Revision: &approval.Card.Revision, Choice: protocol.ApprovalChoiceOnce})
		if err != nil {
			t.Fatal(err)
		}
		frame, err = client.Receive(h.ctx)
		if err != nil || frame.Error != nil || frame.ReplyTo == nil || *frame.ReplyTo != id {
			t.Fatalf("withdrawn approval answer: %+v %v", frame, err)
		}
		approved, ok := frame.Payload.(protocol.ApprovalAnswer)
		if !ok || approved.Outcome == nil || *approved.Outcome == protocol.DecisionOutcomeResolved {
			t.Fatalf("withdrawn approval accepted: %+v", frame)
		}
	}
}
