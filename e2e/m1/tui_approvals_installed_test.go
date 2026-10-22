// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIApprovalsInstalledCore(t *testing.T) {
	t.Parallel()

	var fixture struct {
		ID, Scope, Summary, Target string
		Choice                     protocol.ApprovalChoice
		Outcome                    protocol.DecisionOutcome
	}
	data, err := os.ReadFile("tui-approvals-installed.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { emitResponse(w, "fixture reply", true) }))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	cap := protocol.Capability{Name: "waiting", Version: "1.0"}
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{cap})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !slices.Contains(c.Welcome.Capabilities, cap) {
		t.Fatal("installed Core does not expose waiting decisions")
	}
	// Seed only synthetic producer data after boot recovery. The installed Core
	// owns the authority instance; this test never creates another service or
	// registers a composed handler. All observation/answering uses its socket.
	epoch, err := strconv.ParseUint(c.Welcome.Epoch, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	approval := authority.Approval{ID: fixture.ID, EffectKey: fixture.ID, ScopeKey: fixture.Scope, What: fixture.Summary, Where: fixture.Target, Target: fixture.Target, Person: c.Welcome.PersonID, Agent: c.Welcome.Agents[0].AgentID, Epoch: epoch, State: authority.Pending, Outward: true, Choices: []authority.Choice{authority.Once, authority.Deny}, Arguments: json.RawMessage(`{"body":"fixture document"}`)}
	data, err = json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(h.ctx, "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", approval.ID, approval.EffectKey, "fixture", data); err != nil {
		t.Fatal(err)
	}
	topic := "waiting"
	id, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
	if err != nil {
		t.Fatal(err)
	}
	receive := func(id string) protocol.Envelope {
		t.Helper()
		for range 20 {
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ReplyTo != nil && *frame.ReplyTo == id {
				return frame
			}
		}
		t.Fatal("missing response")
		return protocol.Envelope{}
	}
	reply := receive(id)
	snapshot, ok := reply.Payload.(protocol.Snapshot)
	if !ok || len(snapshot.Items) != 1 {
		t.Fatalf("approval snapshot=%+v", reply)
	}
	var card protocol.ApprovalRequest
	if err = json.Unmarshal(snapshot.Items[0].Payload, &card); err != nil {
		t.Fatal(err)
	}
	if card.Card == nil || card.Card.ID != fixture.ID || len(card.Card.Targets) == 0 {
		t.Fatal("approval card incomplete")
	}
	id, err = c.AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &card.Card.ID, Revision: &card.Card.Revision, Choice: fixture.Choice})
	if err != nil {
		t.Fatal(err)
	}
	reply = receive(id)
	answer, ok := reply.Payload.(protocol.ApprovalAnswer)
	if !ok || answer.Outcome == nil || *answer.Outcome != fixture.Outcome {
		t.Fatalf("approval outcome=%+v", reply)
	}
	var saved []byte
	if err = db.QueryRowContext(h.ctx, "SELECT record FROM authority_approvals WHERE approval_id=?", fixture.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	var record authority.Approval
	if err = json.Unmarshal(saved, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != authority.Approved || record.Choice != authority.Once || record.AnsweredBy != c.Welcome.PersonID || record.Agent != approval.Agent {
		t.Fatalf("authority record=%+v", record)
	}
	// A second synthetic request is answered through the real TUI keyboard,
	// using negotiated event types instead of a timing delay between keys.
	approval.ID += "-tui"
	approval.EffectKey = approval.ID
	approval.What = "TUI: " + fixture.Summary
	data, err = json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(h.ctx, "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", approval.ID, approval.EffectKey, "fixture", data); err != nil {
		t.Fatal(err)
	}
	terminal := h.startScreenTUI("TERM=xterm-256color")
	// The first approval is already resolved and can arrive before the waiting
	// snapshot. Its matching summary must not trigger F6 before the new card.
	readyScreen, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
		cells := screenText(s)
		return s.AltScreen && !s.CursorHidden && strings.Contains(cells, approval.What) &&
			strings.Contains(cells, design.Text(design.English, "live"))
	})
	if err != nil {
		t.Fatalf("pending TUI approval not ready: %v; cells=%s", err, screenText(readyScreen))
	}
	h.keys(terminal, "\x1b[?3u\x1b[17~")
	waitScreen(t, h, terminal, fixture.Target)
	h.keys(terminal, "1")
	waitScreen(t, h, terminal, "[1")
	h.keys(terminal, "\r")
	waitScreen(t, h, terminal, design.Text(design.English, "resolved"))
	if err = db.QueryRowContext(h.ctx, "SELECT record FROM authority_approvals WHERE approval_id=?", approval.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(saved, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != authority.Approved || record.AnsweredBy != c.Welcome.PersonID {
		t.Fatalf("TUI authority decision=%+v", record)
	}
	h.keys(terminal, "\x1b[27u")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	c.Close()
	db.Close()
	h.stopCore(core)
}
