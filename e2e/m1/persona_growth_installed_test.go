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
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2InstalledPersonaGrowth(t *testing.T) {
	t.Parallel()

	var fixture struct {
		Candidate struct {
			ID       string `json:"id"`
			Snapshot string `json:"snapshot"`
			Notice   string `json:"notice"`
		} `json:"candidate"`
	}
	data, err := os.ReadFile("../m2/persona-growth.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var instructions []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Instructions string `json:"instructions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		instructions = append(instructions, body.Instructions)
		first := len(instructions) == 1
		mu.Unlock()
		if first {
			close(started)
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		emitResponse(w, "fixture-reply", true)
	}))
	defer provider.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	conv, conversation := conversationClient(t, h)
	capability := protocol.Capability{Name: "growth", Version: "1.0"}
	connect := func() *clientapi.Client {
		t.Helper()
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{capability})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		if !slices.Contains(c.Welcome.Capabilities, capability) {
			t.Fatal("installed Core has no growth capability")
		}
		return c
	}
	c := connect()
	agent := c.Welcome.Agents[0].AgentID
	request := func(p protocol.AgentRequest) (protocol.AgentResult, *protocol.Error) {
		t.Helper()
		for {
			if _, err := c.Agent(h.ctx, p); err != nil {
				t.Fatal(err)
			}
			reply, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if reply.Error != nil {
				if reply.Error.Code == protocol.ErrorUnavailable {
					t.Fatalf("growth unavailable: %+v", p)
				}
				return protocol.AgentResult{}, reply.Error
			}
			result, ok := reply.Payload.(protocol.AgentResult)
			if !ok {
				t.Fatalf("unexpected growth result: %+v", reply)
			}
			return result, nil
		}
	}
	apply := func(operation string, candidate *protocol.GrowthCandidate, revision *string) protocol.AgentResult {
		t.Helper()
		result, refused := request(protocol.AgentRequest{Operation: &operation, AgentID: &agent, Candidate: candidate, Revision: revision})
		if refused != nil {
			t.Fatalf("growth refused: %+v", refused)
		}
		return result
	}
	key, input := "fixture-before-growth", "fixture-input"
	if _, err = conv.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &input}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal("first turn did not prepare")
	}
	candidate := &protocol.GrowthCandidate{ID: fixture.Candidate.ID, Expected: "1", Snapshot: fixture.Candidate.Snapshot, Notice: fixture.Candidate.Notice}
	applied := apply("growth_adopt", candidate, nil)
	if applied.Revision == nil || *applied.Revision != "2" || applied.Notices == nil || len(*applied.Notices) != 1 {
		t.Fatalf("adopted=%+v", applied)
	}
	undo := (*applied.Notices)[0].UndoRevision
	close(release)
	noticeCount := 0
	for {
		e, err := conv.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == fixture.Candidate.Notice {
			noticeCount++
		}
		if state, ok := e.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	if noticeCount != 1 {
		t.Fatalf("learning lines=%d", noticeCount)
	}
	admittedTurn(t, h, conv, conversation, "fixture-after-growth", "fixture-input-2")
	restored := apply("growth_undo", nil, &undo)
	if restored.Revision == nil || *restored.Revision != "3" || restored.Notices == nil || len(*restored.Notices) != 0 {
		t.Fatalf("undo=%+v", restored)
	}
	admittedTurn(t, h, conv, conversation, "fixture-after-undo", "fixture-input-3")
	mu.Lock()
	captured := append([]string(nil), instructions...)
	mu.Unlock()
	if len(captured) != 3 {
		t.Fatalf("provider calls=%d", len(captured))
	}
	type persona struct {
		AgentID       string        `json:"agent_id"`
		Name          string        `json:"name"`
		OwnerPersonID string        `json:"owner_person_id"`
		Layers        agents.Layers `json:"layers"`
	}
	var before, grown persona
	if err := json.Unmarshal(instructionBlock(captured[0], "persona"), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(instructionBlock(captured[1], "persona"), &grown); err != nil {
		t.Fatal(err)
	}
	want := before
	want.Layers.GrowthSnapshot = candidate.Snapshot
	withoutGrowth := strings.Replace(captured[1], string(instructionBlock(captured[1], "persona")), string(instructionBlock(captured[0], "persona")), 1)
	if grown != want || captured[2] != captured[0] || withoutGrowth != captured[0] {
		t.Fatal("persona changed mid-turn, another layer changed, or undo failed")
	}
	c.Close()
	conv.Close()
	h.stopCore(core)
	// The installed Core commits status with adoption even when there is no
	// worker left to make a separate Report request.
	withFileCanon(t, h, func(db *canon.Store) {
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT record FROM moirai_reports WHERE job=?", "growth:"+agent).Scan(&raw); err != nil {
				return err
			}
			var state jobapi.JobState
			if err := json.Unmarshal(raw, &state); err != nil {
				return err
			}
			if state.Status != "applied" || state.Watermark != 1 || strconv.FormatUint(state.Epoch, 10) != c.Welcome.Epoch {
				t.Fatalf("installed Core lost adoption status: %+v", state)
			}
			return nil
		}); err != nil {
			t.Fatalf("installed Core did not persist adoption status: %v", err)
		}
	})
	if _, err := os.Lstat(filepath.Join(h.root, "run", "jobs.sock")); !os.IsNotExist(err) {
		t.Fatalf("job socket left after shutdown: %v", err)
	}
	core = start(true)
	conv, conversation = conversationClient(t, h)
	c = connect()
	candidate.ID = "fixture-renamed"
	candidate.Expected = "3"
	suppressed := apply("growth_adopt", candidate, nil)
	if suppressed.Revision == nil || *suppressed.Revision != "3" || suppressed.Notices == nil || len(*suppressed.Notices) != 0 {
		t.Fatalf("undone step reproposed=%+v", suppressed)
	}
	operation, foreign := "growth_notices", "fixture-foreign-agent"
	if _, refused := request(protocol.AgentRequest{Operation: &operation, AgentID: &foreign}); refused == nil || refused.Code != protocol.ErrorOutOfScope {
		t.Fatalf("foreign selector=%+v", refused)
	}
	// Invalid candidates roll back their queue row so they cannot poison later work.
	candidate.ID = "fixture-invalid"
	candidate.Snapshot = "fixture-next"
	candidate.Notice = "first\nsecond"
	operation = "growth_adopt"
	if _, refused := request(protocol.AgentRequest{Operation: &operation, AgentID: &agent, Candidate: candidate}); refused == nil || refused.Code != protocol.ErrorMalformed {
		t.Fatalf("invalid candidate=%+v", refused)
	}
	candidate.ID = "fixture-next"
	candidate.Notice = fixture.Candidate.Notice + "-next"
	next := apply("growth_adopt", candidate, nil)
	if next.Revision == nil || *next.Revision != "4" {
		t.Fatalf("queue blocked by invalid candidate: %+v", next)
	}
	// The original learning notice survives restart exactly once in history.
	historyClient, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer historyClient.Close()
	topic := "conversation:" + conversation
	if _, err = historyClient.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	history, err := historyClient.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(history.Payload)
	if strings.Count(string(raw), `"text":"`+fixture.Candidate.Notice+`"`) != 1 {
		t.Fatalf("learning history missing or duplicated: %s", raw)
	}
	c.Close()
	conv.Close()
	h.stopCore(core)
	// Growth control remains usable without configuring a model account/route.
	core = start(false)
	c = connect()
	operation = "growth_notices"
	for {
		if _, err = c.Agent(h.ctx, protocol.AgentRequest{Operation: &operation}); err != nil {
			t.Fatal(err)
		}
		ready, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ready.Error != nil && ready.Error.Code == protocol.ErrorMalformed {
			break
		}
		if ready.Error == nil || ready.Error.Code != protocol.ErrorUnavailable {
			t.Fatalf("unexpected readiness: %+v", ready)
		}
	}
	withoutModel := apply("growth_notices", nil, nil)
	if withoutModel.Revision == nil || *withoutModel.Revision != "4" {
		t.Fatalf("growth requires a model: %+v", withoutModel)
	}
	c.Close()
	h.stopCore(core)
}
