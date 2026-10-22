// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	builtin "github.com/thisisjun786/lina/skills"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

type codexWorkScenario struct {
	Kind                           string `json:"kind,omitempty"`
	Input, Result, Artifact        string
	Question, Answer, Coordination string
	TaskPacket                     string  `json:"task_packet"`
	FiveHour                       float64 `json:"five_hour"`
	Weekly                         float64
}

func codexWorkToolResult(raw json.RawMessage, out any) error {
	var result wire.DynamicToolCallResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Success {
		for _, item := range result.ContentItems {
			if item.InputText != nil {
				return json.Unmarshal([]byte(item.InputText.Text), out)
			}
		}
	}
	return fmt.Errorf("work tool did not return successful JSON text: %s", raw)
}

// The installed executable is this test binary in fixture mode. Only Codex and
// the model endpoint are synthetic; Core, authority, storage and stdio are real.
func codexWorkToolsFixture() {
	if os.Args[1] == "--version" {
		fmt.Println("codex-cli 99.0.0")
		return
	}
	if len(os.Args) != 4 || strings.Join(os.Args[1:], " ") != "app-server --listen stdio://" || os.Getenv("LINA_RESPONSES_API_KEY") != "" || os.Getenv("CODEX_HOME") != "/fixture/codex-home" {
		os.Exit(91)
	}
	var fixture codexWorkScenario
	if json.Unmarshal([]byte(os.Getenv("LINA_WORK_TOOLS_SCENARIO")), &fixture) != nil {
		os.Exit(92)
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(93)
		}
	}
	notify := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	const thread, turn = "work-tools-thread", "work-tools-turn"
	const approvalID, projectionID = "71", `"projection-rpc"`
	const askID, coordinateID, receiptID = `"ask-rpc"`, `"coordinate-rpc"`, `"receipt-rpc"`
	initialized, approved, completed := false, false, false
	cwd, assignment, pendingTool, receivedPacket := "", "", "", ""
	callTool := func(id, name string, args any) {
		pendingTool = id
		send(map[string]any{"id": json.RawMessage(id), "method": "item/tool/call", "params": map[string]any{
			"threadId": thread, "turnId": turn, "callId": name + "-call", "namespace": "lina", "tool": name, "arguments": args,
		}})
	}
	var receipt work.Receipt
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var r struct {
			ID     json.RawMessage
			Method string
			Params map[string]json.RawMessage
			Result json.RawMessage
			Error  json.RawMessage
		}
		if json.Unmarshal(scan.Bytes(), &r) != nil {
			os.Exit(94)
		}
		reply := func(result any) { send(map[string]any{"id": r.ID, "result": result}) }
		fail := func(reason string) {
			send(map[string]any{"id": r.ID, "error": map[string]any{"code": -32602, "message": reason}})
			os.Exit(95)
		}
		switch r.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "fixture/99.0.0"})
		case "initialized":
			initialized = true
		case "thread/start":
			if !initialized || r.Params["cwd"] == nil || r.Params["dynamicTools"] == nil {
				fail("thread/start must include cwd and dynamicTools")
			}
			for key := range r.Params {
				if key != "cwd" && key != "dynamicTools" {
					fail("thread/start overwrote installed settings: " + key)
				}
			}
			var specs []wire.V2DynamicToolSpec
			if json.Unmarshal(r.Params["dynamicTools"], &specs) != nil || json.Unmarshal(r.Params["cwd"], &cwd) != nil || !filepath.IsAbs(cwd) {
				fail("invalid dynamic tools or cwd")
			}
			names := map[string]bool{}
			for _, spec := range specs {
				if spec.Namespace != nil && spec.Namespace.Name == "lina" {
					for _, tool := range spec.Namespace.Tools {
						if tool.Function != nil {
							names[tool.Function.Name] = true
						}
					}
				}
			}
			for _, name := range []string{"read_projection", "ask", "coordinate", "submit_receipt"} {
				if !names[name] {
					fail("missing lina dynamic tool: " + name)
				}
			}
			reply(map[string]any{"thread": map[string]any{"id": thread}, "cwd": cwd, "model": "worker-own-model"})
		case "turn/start":
			var params wire.V2TurnStartParams
			raw, _ := json.Marshal(r.Params)
			if json.Unmarshal(raw, &params) != nil || len(r.Params) != 2 || params.ThreadId != thread || len(params.Input) != 1 || params.Input[0].Text == nil {
				fail("turn/start must contain only bound thread and task packet")
			}
			receivedPacket = params.Input[0].Text.Text
			if fixture.Kind == "" {
				if receivedPacket != fixture.TaskPacket {
					fail("task packet changed")
				}
			} else {
				var packet struct {
					Task   string      `json:"task_packet"`
					Method skills.Body `json:"method"`
				}
				body, err := fs.ReadFile(builtin.Builtins(), "app-debugging/SKILL.md")
				if err != nil || json.Unmarshal([]byte(receivedPacket), &packet) != nil || packet.Task != fixture.TaskPacket || packet.Method.Version.ID != "app-debugging" || packet.Method.Version.Commit != "builtin:"+builtin.Builtins().SkillRevision() || packet.Method.Files["SKILL.md"].Text != string(body) {
					fail("worker did not receive the selected immutable skill body")
				}
				entries, err := os.ReadDir(cwd)
				if err != nil || len(entries) != 0 {
					fail("method installed files into the worker workdir")
				}
			}
			notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress"}})
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
			send(map[string]any{"id": json.RawMessage(approvalID), "method": "item/commandExecution/requestApproval", "params": map[string]any{
				"threadId": thread, "turnId": turn, "itemId": "artifact-command", "startedAtMs": 1800000000000,
				"command": "write result.txt", "cwd": cwd, "availableDecisions": []string{"accept", "decline"},
			}})
		case "":
			if len(r.Error) != 0 {
				fail("Core rejected worker request: " + string(r.Error))
			}
			if string(r.ID) != approvalID && (pendingTool == "" || string(r.ID) != pendingTool) {
				fail("Core changed server request id or reordered tool responses: " + string(r.ID))
			}
			switch string(r.ID) {
			case approvalID:
				var answer struct{ Decision string }
				if approved || json.Unmarshal(r.Result, &answer) != nil || answer.Decision != "accept" {
					fail("Once did not return accept for the exact numeric approval id")
				}
				approved = true
				callTool(projectionID, "read_projection", map[string]any{})
			case projectionID:
				var projection struct {
					TaskPacket   string `json:"task_packet"`
					AssignmentID string `json:"assignment_id"`
				}
				if !approved || codexWorkToolResult(r.Result, &projection) != nil || projection.TaskPacket != receivedPacket || projection.AssignmentID == "" {
					fail("projection lacks assigned task_packet and assignment_id after approval")
				}
				assignment = projection.AssignmentID
				callTool(askID, "ask", map[string]any{"question": fixture.Question})
			case askID:
				var answer struct{ Answer string }
				if codexWorkToolResult(r.Result, &answer) != nil || answer.Answer != fixture.Answer {
					fail("ask did not return the originating person's answer")
				}
				callTool(coordinateID, "coordinate", map[string]any{"request": fixture.Coordination})
			case coordinateID:
				var coordinated struct {
					Accepted bool
					EventID  string `json:"event_id"`
				}
				if codexWorkToolResult(r.Result, &coordinated) != nil || !coordinated.Accepted || coordinated.EventID == "" {
					fail("coordinate did not acknowledge a recorded request")
				}
				receipt = work.Receipt{SchemaVersion: 1, RelationshipID: assignment, ExecutionGeneration: 1, Attempt: 1,
					RevisionHash: strings.Repeat("0", 64), Outcome: "blocked_needs_input", Producer: "child",
					TurnRef:   work.TurnRef{ThreadID: thread, TurnID: turn, TurnStatus: "completed"},
					EmittedAt: time.Unix(1800000000, 0).UTC(), Manifest: []work.Artifact{}, Provenance: []work.Provenance{}}
				receipt.EventID = work.ReceiptID(receipt)
				callTool(receiptID, "submit_receipt", map[string]any{"receipt": receipt})
			case receiptID:
				var claim struct {
					Accepted bool
					Verified *bool
					EventID  string `json:"event_id"`
				}
				if codexWorkToolResult(r.Result, &claim) != nil || !claim.Accepted || claim.Verified == nil || *claim.Verified || !strings.HasSuffix(claim.EventID, receipt.EventID) {
					fail("submit_receipt did not retain an unverified completion claim")
				}
				pendingTool = ""
				if os.WriteFile(filepath.Join(cwd, fixture.Artifact), []byte(fixture.Result), 0600) != nil {
					fail("cannot write assigned artifact")
				}
				notify("item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"type": "agentMessage", "id": "artifact-result", "text": fixture.Result}})
				completed = true
				notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed"}})
				// This HTTP handshake closes a test-owned channel only after both
				// notifications were written. No polling or elapsed delay gates quota.
				client := &http.Client{Timeout: operationTimeout}
				response, err := client.Post(os.Getenv("LINA_WORKER_BARRIER_URL"), "text/plain", nil)
				if err != nil {
					fail("worker completion barrier failed")
				}
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					fail("worker completion barrier refused")
				}
			default:
				fail("Core changed server request id: " + string(r.ID))
			}
		case "thread/read":
			status, state := "inProgress", "active"
			if completed {
				status, state = "completed", "idle"
			}
			reply(map[string]any{"thread": map[string]any{"id": thread, "cwd": cwd, "status": map[string]any{"type": state, "activeFlags": []string{}}, "turns": []any{map[string]any{"id": turn, "status": status}}}})
		case "account/rateLimits/read":
			if !completed {
				fail("quota requested before worker completion")
			}
			reply(map[string]any{"accountId": "work-tools-account", "rateLimits": map[string]any{
				"primary":   map[string]any{"usedPercent": fixture.FiveHour, "windowDurationMins": 300},
				"secondary": map[string]any{"usedPercent": fixture.Weekly, "windowDurationMins": 10080},
			}})
		default:
			fail("unexpected Codex method: " + r.Method)
		}
	}
	if scan.Err() != nil {
		os.Exit(96)
	}
}

func TestM2CodexWorkTools(t *testing.T) {
	t.Parallel()
	runCodexWorkTools(t, "../m2/codex-work-tools.json", "", nil)
}

func TestM2CodexDevelopmentMethod(t *testing.T) {
	t.Parallel()
	runCodexWorkTools(t, "../m2/codex-work-tools.json", "bug_unknown_cause", nil)
}

func runCodexWorkTools(t *testing.T, scenario, kind string, observe func(*harness, string)) {
	t.Helper()
	data, err := os.ReadFile(scenario)
	if err != nil {
		t.Fatal(err)
	}
	var fixture codexWorkScenario
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Kind = kind
	data, err = json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	finished := make(chan struct{})
	var calls atomic.Int32
	var quotaChecked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker-finished" {
			close(finished)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		for _, raw := range req.Tools {
			var tool struct{ Name string }
			if err := json.Unmarshal(raw, &tool); err != nil {
				t.Error(err)
				return
			}
			if strings.Contains(tool.Name, "approv") || tool.Name == "worker_answer" {
				t.Errorf("model can approve worker requests through %s", tool.Name)
			}
		}
		n := calls.Add(1)
		if n == 1 {
			emitPersonaCall(w, "start-worker", "worker_start", map[string]any{"workdir": workdir, "task_packet": fixture.TaskPacket, "kind": kind})
			return
		}
		var item struct{ Output string }
		var result struct {
			Code   string
			Output json.RawMessage
		}
		if len(req.Input) == 0 || json.Unmarshal(req.Input[len(req.Input)-1], &item) != nil || json.Unmarshal([]byte(item.Output), &result) != nil || result.Code != "ok" {
			t.Errorf("worker tool step %d failed: %s", n, item.Output)
			emitResponse(w, "worker tool failed", true)
			return
		}
		if n == 2 {
			var session struct{ Session string }
			if json.Unmarshal(result.Output, &session) != nil || session.Session == "" {
				t.Errorf("worker_start omitted session: %s", result.Output)
				emitResponse(w, "worker session missing", true)
				return
			}
			select {
			case <-finished:
			case <-r.Context().Done():
				return
			case <-t.Context().Done():
				return
			}
			emitPersonaCall(w, "quota-worker", "worker_quota", map[string]any{"session": session.Session})
			return
		}
		var quota struct{ Rows []provider.QuotaRow }
		if n != 3 || json.Unmarshal(result.Output, &quota) != nil {
			t.Errorf("unexpected quota response at step %d: %s", n, result.Output)
			emitResponse(w, "worker quota failed", true)
			return
		}
		found := map[provider.Window]float64{}
		for _, row := range quota.Rows {
			if row.UsedPct != nil {
				found[row.Window] = *row.UsedPct
			}
		}
		if found[provider.FiveHour] != fixture.FiveHour || found[provider.Weekly] != fixture.Weekly {
			t.Errorf("quota rows=%s", result.Output)
		} else {
			quotaChecked.Store(true)
		}
		emitResponse(w, "worker quota checked", true)
	}))
	defer func() { server.CloseClientConnections(); server.Close() }()
	h, bootstrap := installedConversationHarness(t, server.URL)
	seedCodingFixture(t, h, bootstrap)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=2", "CODEX_HOME=/fixture/codex-home", "LINA_WORK_TOOLS_SCENARIO="+string(data), "LINA_WORKER_BARRIER_URL="+server.URL+"/worker-finished", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connect(p).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}, {Name: "waiting", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.History(h.ctx, protocol.HistoryGet{}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := frame.Payload.(protocol.History)
	if !ok || history.ConversationID == nil {
		t.Fatal("no originating conversation")
	}
	conversation := *history.ConversationID
	for _, topic := range []string{"conversation:" + conversation, "waiting"} {
		if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		if _, err = c.Receive(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	key := "codex-work-tools"
	inputID, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &fixture.Input})
	if err != nil {
		t.Fatal(err)
	}
	ack, done, notice, resolved, questionResolved := false, false, false, false, false
	answerNotice, answerNoticeKey := "", ""
	answerID, questionAnswerID := "", ""
	for !ack || !done || !notice || !resolved || !questionResolved || answerNotice == "" {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatalf("worker journey ack=%t done=%t notice=%t resolved=%t questionResolved=%t: %v", ack, done, notice, resolved, questionResolved, err)
		}
		if frame.Error != nil {
			t.Fatalf("worker journey protocol error: %+v", frame.Error)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == inputID {
			ack = true
		}
		switch payload := frame.Payload.(type) {
		case protocol.ApprovalRequest:
			if payload.Card == nil || payload.ConversationID == nil || *payload.ConversationID != conversation || !slices.Contains(payload.Choices, protocol.ApprovalChoiceOnce) {
				t.Fatalf("worker approval lost originating conversation or Once choice: %+v", payload)
			}
			if answerID != "" {
				continue // A waiting snapshot may repeat the same pending card.
			}
			if observe != nil {
				observe(h, "approval")
			}
			answerID, err = c.AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &payload.Card.ID, Revision: &payload.Card.Revision, Choice: protocol.ApprovalChoiceOnce})
			if err != nil {
				t.Fatal(err)
			}
		case protocol.ApprovalAnswer:
			if frame.ReplyTo != nil && *frame.ReplyTo == answerID {
				if payload.Outcome == nil || *payload.Outcome != protocol.DecisionOutcomeResolved {
					t.Fatalf("person's approval not resolved: %+v", payload)
				}
				resolved = true
			}
		case protocol.Question:
			if payload.Card == nil || payload.ConversationID != conversation || payload.Card.Summary != fixture.Question || payload.Form == nil || len(payload.Form.Fields) != 1 || payload.Form.Fields[0].Name != "answer" || payload.Form.Fields[0].Type != protocol.FormFieldTypeText {
				t.Fatalf("worker question lost origin or answer field: %+v", payload)
			}
			if questionAnswerID != "" {
				continue
			}
			if observe != nil {
				observe(h, "question")
			}
			values := protocol.FormValues{"answer": protocol.FormValue{String: &fixture.Answer}}
			answerNoticeKey = payload.Card.ID + ":answer"
			questionAnswerID, err = c.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &payload.Card.ID, Revision: &payload.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values})
			if err != nil {
				t.Fatal(err)
			}
		case protocol.QuestionAnswer:
			if frame.ReplyTo != nil && *frame.ReplyTo == questionAnswerID {
				if payload.Outcome == nil || *payload.Outcome != protocol.DecisionOutcomeResolved {
					t.Fatalf("person's question answer not resolved: %+v", payload)
				}
				questionResolved = true
			}
		case protocol.ItemDone:
			if payload.Stream != nil && payload.Stream.Key == answerNoticeKey {
				assertWorkerAnswerNotice(t, payload)
				answerNotice = *payload.Text
			}
			notice = notice || payload.Text != nil && strings.Contains(*payload.Text, fixture.Result)
		case protocol.TurnState:
			if payload.Active != nil && !*payload.Active {
				done = true
				if !quotaChecked.Load() {
					t.Fatal("conversation ended before approved worker result and quota were checked")
				}
			}
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("provider steps=%d, want 3", calls.Load())
	}
	artifact, err := os.ReadFile(filepath.Join(workdir, fixture.Artifact))
	if err != nil || string(artifact) != fixture.Result {
		t.Fatalf("real worker artifact=%q: %v", artifact, err)
	}
	if observe != nil {
		observe(h, "completed")
	}
	c.Close()
	h.stopCore(core)
	core = start()
	if observe != nil {
		observe(h, "restarted")
	}
	c, restored := conversationClient(t, h)
	if restored != conversation {
		t.Fatal("Core restart lost originating conversation")
	}
	topic := "conversation:" + conversation
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err = c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(frame.Payload)
	if err != nil || !strings.Contains(string(raw), fixture.Result) {
		t.Fatalf("worker finish notice not durable: %s: %v", raw, err)
	}
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if !ok {
		t.Fatalf("expected restored conversation snapshot, got %T", frame.Payload)
	}
	restoredAnswer := false
	for _, item := range snapshot.Items {
		if item.Key != answerNoticeKey {
			continue
		}
		var payload protocol.ItemDone
		if err := json.Unmarshal(item.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		assertWorkerAnswerNotice(t, payload)
		if *payload.Text != answerNotice {
			t.Fatalf("answer notice changed after restart: %q -> %q", answerNotice, *payload.Text)
		}
		restoredAnswer = true
	}
	if !restoredAnswer {
		t.Fatal("worker answer acknowledgement missing after Core restart")
	}
	c.Close()
	h.stopCore(core)
}

func assertWorkerAnswerNotice(t *testing.T, notice protocol.ItemDone) {
	t.Helper()
	if notice.Speaker == nil || *notice.Speaker != protocol.ConversationSpeakerSystem || notice.Text == nil || strings.TrimSpace(*notice.Text) == "" {
		t.Fatalf("missing worker answer acknowledgement: %+v", notice)
	}
	// Check the display boundary, not the wording of the acknowledgement.
	for _, internal := range []string{"contentItems", "inputText", `"success":`, `"action":`, `"answer":`} {
		if strings.Contains(*notice.Text, internal) {
			t.Errorf("worker answer notice exposes transport field %q: %s", internal, *notice.Text)
		}
	}
}
