// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3DependencyReleaseInstalledCore (m3/dependency-release.json) runs the
// installed Core with a synthetic App Server and forge. Conversation tools
// admit a graph, Core creates actual Git worktrees, and worker processes write
// and commit results after a real client answers a worker question and
// approval. Independent work starts while a dependent waits; only a merge in a
// separate forge clone followed by Core's observation releases the dependent
// onto the fetched remote base. The parent clone's checked-out base branch
// stays unchanged. Restart retains the merge without turning it into a delivery
// verdict. The fixture has no provider credential or external GitHub effect.
// m3/dependency-area-overlap.json first submits unordered areas for a
// wildcard-matched directory and a nested file. Core must reject the graph
// before the ordered directory/child graph in the release scenario can execute.
// Ordinary Git commands in the scheduled worktree must refuse base and
// tracking-ref writes, another task's branch update, and deletion of its own
// assigned branch. The same checks run after restart, while real worker
// commits, pushes, and Core's dependent allocation continue through the
// protected parent clone.
//
// The worker also calls all four LINA work tools, receives both work-tool and
// native question answers, and submits a receipt for its committed artifact.
func TestM3DependencyReleaseInstalledCore(t *testing.T) {
	testM3DependencyRelease(t, nil, nil, "", "")
}

// TestM3DelegationManagerAdmission (m3/dependency-manager.json) exercises graph
// admission from a non-dev persona, durable dev ownership and assignment before
// dispatch, and missing-dev refusal without work cards or worker effects.
// Questions, approvals, refresh, stop controls, and restart retain the original
// conversation.
func TestM3DelegationManagerAdmission(t *testing.T) {
	raw, err := os.ReadFile("../m3/dependency-manager.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name       string
			MissingDev bool `json:"missing_dev"`
		}
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, c := range scenario.Cases {
		t.Run(c.Name, func(t *testing.T) {
			testM3DependencyRelease(t, nil, nil, "", "", dependencyReleaseOptions{missingManager: c.MissingDev})
		})
	}
}

// TestM3DelegationPresetLimits (m3/delegation-preset-limits.json) calls the
// configured graph tool through the installed Core as investing and legal
// presets. Both must receive a policy refusal before task admission, with no
// queued work appearing after restart.
func TestM3DelegationPresetLimits(t *testing.T) {
	data, err := os.ReadFile("../m3/delegation-preset-limits.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Presets []string }
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, preset := range scenario.Presets {
		t.Run(preset, func(t *testing.T) { testM3DependencyRelease(t, nil, nil, preset, "") })
	}
}

type delegationConflict struct {
	Mergeable string `json:"mergeable"`
	Returns   int    `json:"returns"`
}

// TestM3DependencyConflictReturnsOnce (m3/dependency-conflict.json) keeps the
// predecessor pull request unmergeable through a correction turn. Core returns
// it to the same worker exactly once, requires input for the remaining
// conflict, and retains the dependent's gate until the forge merge. Repeated
// observation and restart cannot resend work.
func TestM3DependencyConflictReturnsOnce(t *testing.T) {
	raw, err := os.ReadFile("../m3/dependency-conflict.json")
	if err != nil {
		t.Fatal(err)
	}
	var conflict delegationConflict
	if err = json.Unmarshal(raw, &conflict); err != nil {
		t.Fatal(err)
	}
	testM3DependencyRelease(t, nil, &conflict, "", "")
}

type delegationStartFailure struct {
	Command string `json:"command"`
	Slots   int    `json:"slots"`
	Restart bool   `json:"restart"`
}

// TestM3DelegationStartFailureReleasesCapacity
// (m3/dependency-start-failure.json) injects a failed initial Git fetch with
// one host slot: another task must finish and submit its receipt, while restart
// must retain the failed attempt without its reservations or a duplicate
// dispatch.
func TestM3DelegationStartFailureReleasesCapacity(t *testing.T) {
	testM3DelegationStartFailure(t, "dependency-start-failure.json")
}

// TestM3DelegationRecoveredFailureKeepsRuntime
// (m3/dependency-recovery-failure.json) persists a graph with no capacity, then
// restarts Core with one slot and a single failing Git fetch. It requires graph
// tools to remain available, the independent worker to commit and submit its
// receipt, and the settled failure to survive another restart without replay.
// The conversation client correlates each readiness probe's reply by request
// id; worker notices and late replies cannot satisfy or reject that probe. A
// scripted regression interleaves those frames without depending on scheduler
// timing.
func TestM3DelegationRecoveredFailureKeepsRuntime(t *testing.T) {
	testM3DelegationStartFailure(t, "dependency-recovery-failure.json")
}

func testM3DelegationStartFailure(t *testing.T, scenario string) {
	t.Helper()
	raw, err := os.ReadFile("../m3/" + scenario)
	if err != nil {
		t.Fatal(err)
	}
	var fault delegationStartFailure
	if err := json.Unmarshal(raw, &fault); err != nil {
		t.Fatal(err)
	}
	testM3DependencyRelease(t, &fault, nil, "", "")
}

type dependencyReleaseOptions struct {
	missingManager bool
	outcomes       scheduledOutcomeScenario
}

func testM3DependencyRelease(t *testing.T, fault *delegationStartFailure, conflict *delegationConflict, preset, stopMode string, options ...dependencyReleaseOptions) {
	var option dependencyReleaseOptions
	if len(options) != 0 {
		option = options[0]
	}
	outcome, noDev := option.outcomes, option.missingManager
	var recalling atomic.Bool
	raw, err := os.ReadFile("../m3/dependency-release.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario any
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	overlap, err := os.ReadFile("../m3/dependency-area-overlap.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var managerCalls atomic.Int32
	var taskIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage
			Tools []struct{ Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			// Restart can cancel a completion follow-up while its request body
			// is still being sent. Keep reporting malformed live requests.
			if r.Context().Err() == nil {
				t.Error(err)
			}
			return
		}
		for _, tool := range request.Tools {
			if tool.Name == "coding_decision" {
				managerCalls.Add(1)
				scriptedCodingManager(t, w, request)
				return
			}
		}
		if noDev {
			if calls.Add(1) == 1 {
				emitPersonaCall(w, "missing-manager", "schedule_work", scenario)
			} else {
				var item struct{ Output string }
				_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
				var result struct {
					Code   string
					Output struct{ Code string }
				}
				if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != "failed" || result.Output.Code != "dev_agent_unavailable" {
					t.Errorf("missing dev did not report admission refusal: %s %v", item.Output, err)
				}
				emitResponse(w, "The dev manager is unavailable; no worker started.", true)
			}
			return
		}
		if outcome != nil && recalling.Load() {
			recallScheduledOutcomes(t, w, request.Input, outcome, taskIDs)
			return
		}
		if outcome != nil && respondScheduledOutcome(t, w, request.Input, outcome) {
			return
		}
		if respondWorkerCompletion(w, request.Input, "") {
			return
		}
		if preset != "" {
			if calls.Add(1) == 1 {
				declared := false
				for _, tool := range request.Tools {
					declared = declared || tool.Name == "schedule_work"
				}
				if !declared {
					t.Error("scenario did not reach the configured graph tool")
				}
				emitPersonaCall(w, "restricted-graph", "schedule_work", scenario)
			} else {
				var item struct{ Output string }
				_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
				var result struct{ Code string }
				if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != "denied" {
					t.Errorf("restricted preset dispatched graph: %s %v", item.Output, err)
				}
				emitResponse(w, "Worker execution refused by the originating preset.", true)
			}
			return
		}
		switch n := calls.Add(1); n {
		case 1:
			var cycle map[string]any
			_ = json.Unmarshal(raw, &cycle)
			tasks := cycle["tasks"].([]any)
			tasks[0].(map[string]any)["depends_on"] = []string{"b"}
			emitPersonaCall(w, "cycle", "schedule_work", cycle)
		case 2:
			var item struct{ Output string }
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var result struct{ Code string }
			_ = json.Unmarshal([]byte(item.Output), &result)
			if result.Code != "invalid_arguments" {
				t.Errorf("Core accepted dependency cycle: %s", item.Output)
			}
			emitPersonaCall(w, "area-overlap", "schedule_work", json.RawMessage(overlap))
		case 3:
			var item struct{ Output string }
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var result struct{ Code string }
			_ = json.Unmarshal([]byte(item.Output), &result)
			if result.Code != "invalid_arguments" {
				t.Errorf("Core admitted unordered directory/descendant areas: %s", item.Output)
			}
			emitPersonaCall(w, "dispatch", "schedule_work", scenario)
		case 4:
			var item struct{ Output string }
			if len(request.Input) == 0 {
				t.Error("no tool result")
				emitResponse(w, "Failed.", true)
				return
			}
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var result struct {
				Code   string
				Output struct{ Result []string }
			}
			want := "ok"
			if fault != nil && !fault.Restart {
				want = "failed"
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != want {
				t.Errorf("dispatch result: %s %v", item.Output, err)
			}
			taskIDs = result.Output.Result
			emitResponse(w, "Work started.", true)
		case 5, 7:
			if fault != nil && fault.Restart {
				available := false
				for _, tool := range request.Tools {
					available = available || tool.Name == "schedule_work"
				}
				if !available {
					t.Error("recovered task failure disabled delegation tools")
				}
			}
			emitPersonaCall(w, "refresh", "refresh_work", map[string]any{"tasks": taskIDs})
		default:
			emitResponse(w, "Work refreshed.", true)
		}
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	program, err := filepath.Abs("../m3/dependency-worker.mjs")
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(h.root, "work", "clones", "fixture")
	if err = os.MkdirAll(clone, 0700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(h.ctx, git, append([]string{"-C", clone}, args...)...)
		cmd.Env = []string{"HOME=" + h.root, "PATH=" + filepath.Dir(git), "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "dev")
	runGit("config", "user.name", "Fixture")
	runGit("config", "user.email", "fixture@example.invalid")
	if err = os.WriteFile(filepath.Join(clone, "DESIGN.md"), []byte("# Design\n\n## Work\n\nProduce committed artifacts.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "fixture base")
	initialBase := runGit("rev-parse", "dev")
	remote := filepath.Join(h.root, "forge.git")
	runGit("init", "--bare", remote)
	runGit("remote", "add", "origin", remote)
	runGit("push", "origin", "dev")
	forge := filepath.Join(h.root, "forge")
	runGit("clone", "--branch", "dev", remote, forge)

	writeJSON := func(name string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(filepath.Join(h.root, "fixture.json"), map[string]any{"git": git, "clone": clone, "forge": forge, "requests": outcome == nil && fault == nil && conflict == nil && stopMode == "", "conflict": conflict, "stop": stopMode != "", "outcomes": outcome})
	gh := filepath.Join(h.root, "fixture-gh")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err = os.WriteFile(gh, []byte(fmt.Sprintf("#!/bin/sh\nexec %s %s %s forge \"$@\"\n", quote(node), quote(program), quote(h.root))), 0700); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(h.root, "fixture-codex")
	workerLog := filepath.Join(h.root, "fixture-worker-errors.log")
	if err = os.WriteFile(codex, []byte(fmt.Sprintf("#!/bin/sh\nexec %s %s %s \"$@\" 2>>%s\n", quote(node), quote(program), quote(h.root), quote(workerLog))), 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"codex": codex, "git": git, "gh": gh, "slots": 4, "repositories": map[string]any{"fixture/repo": map[string]string{"clone": clone, "base": "dev"}}}
	if stopMode != "" {
		config["slots"] = 1
	}
	if fault != nil {
		failingGit := filepath.Join(h.root, "fixture-git")
		marker := filepath.Join(h.root, "fetch-failed")
		script := fmt.Sprintf("#!/bin/sh\nfor arg do\nif [ \"$arg\" = %s ] && [ ! -f %s ]; then : > %s; exit 1; fi\ndone\nexec %s \"$@\"\n", quote(fault.Command), quote(marker), quote(marker), quote(git))
		if err = os.WriteFile(failingGit, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		config["git"], config["slots"] = failingGit, fault.Slots
		if fault.Restart {
			config["slots"] = 0
		}
	}
	writeJSON(filepath.Join(h.root, "system", "core", "delegation.json"), config)
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "LINA_RESPONSES_BASE_URL="+server.URL, "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=fixture-key")
		p.start(t)
		t.Cleanup(func() {
			if t.Failed() {
				p.killAndWait()
				t.Logf("Core stderr: %s", &p.stderr)
				if body, err := os.ReadFile(workerLog); err == nil && len(body) > 0 {
					t.Logf("fixture worker stderr: %s", body)
				}
			}
		})
		h.connectWithin(p, 30*time.Second).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	selected := ""
	if preset != "" || noDev {
		probe, _ := conversationClient(t, h)
		person := probe.Welcome.PersonID
		probe.Close()
		h.stopCore(core)
		if noDev {
			withFileCanon(t, h, func(db *canon.Store) {
				if err := db.Write(h.ctx, func(tx *canon.Tx) error {
					_, err := tx.ExecContext(h.ctx, "UPDATE agents SET trashed_at=1 WHERE preset='dev'")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			})
		} else {
			selected = seedMethodologyAgents(t, h, person, preset)[preset]
		}
		core = start()
	}
	client, conversation := conversationClient(t, h)
	if selected != "" && conversation != "main:"+selected {
		t.Fatal("preset scenario used a different conversation")
	}
	waiting, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "waiting", Version: "1.0"}, {Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Close()
	topic := "waiting"
	if _, err = waiting.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if _, err = waiting.Receive(h.ctx); err != nil {
		t.Fatal(err)
	}
	admittedTurn(t, h, client, conversation, "dispatch-input", "Complete the three related tasks.")
	if preset != "" || noDev {
		db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.Background())
		store := work.New(db)
		assertEmpty := func() {
			t.Helper()
			cards, err := store.Cards(h.ctx)
			if err != nil || len(cards) != 0 {
				t.Fatalf("refused graph admitted work: cards=%d err=%v", len(cards), err)
			}
		}
		assertEmpty()
		if noDev {
			var effects int
			if err := db.Read(h.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(h.ctx, `SELECT count(*) FROM authority_effects WHERE json_extract(record,'$.Tool') IN ('worker_start','schedule_work')`).Scan(&effects)
			}); err != nil || effects != 0 || managerCalls.Load() != 0 {
				t.Fatalf("missing dev produced a worker effect: effects=%d manager calls=%d err=%v", effects, managerCalls.Load(), err)
			}
		}
		client.Close()
		waiting.Close()
		h.stopCore(core)
		core = start()
		assertEmpty()
		h.stopCore(core)
		return
	}
	if fault != nil && fault.Restart {
		// Persist queued work first; the injected failure must occur inside
		// startup recovery, alongside an independent successful dispatch.
		client.Close()
		waiting.Close()
		h.stopCore(core)
		config["slots"] = fault.Slots
		writeJSON(filepath.Join(h.root, "system", "core", "delegation.json"), config)
		core = start()
		client, conversation = conversationClient(t, h)
		admittedTurn(t, h, client, conversation, "recovery-input", "Check the recovered work.")
		if t.Failed() {
			client.Close()
			h.stopCore(core)
			t.FailNow()
		}
	}
	answeredQuestions := map[string]bool{}
	var withdrawnQuestion *protocol.Question
	var withdrawnApproval *protocol.ApprovalRequest
	withdrawalChecked := false
	approvalAnswered := false
	wantQuestions := 2
	if outcome != nil || fault != nil || conflict != nil || stopMode != "" {
		wantQuestions, approvalAnswered = 0, true
	}
	answerCtx, stopAnswers := context.WithTimeout(h.ctx, operationTimeout)
	defer stopAnswers()
	for len(answeredQuestions) < wantQuestions || !approvalAnswered {
		frame, err := waiting.Receive(answerCtx)
		if err != nil {
			t.Fatalf("scheduled requests not delivered: questions=%d approval=%v: %v", len(answeredQuestions), approvalAnswered, err)
		}
		if frame.Error != nil {
			t.Fatal(frame.Error)
		}
		switch card := frame.Payload.(type) {
		case protocol.Question:
			if card.Form != nil && len(card.Form.Fields) == 1 && card.Form.Fields[0].Name == "withdrawn" {
				withdrawnQuestion = &card
				continue
			}
			if card.Card != nil && answeredQuestions[card.Card.ID] {
				continue
			}
			if card.Card == nil || card.ConversationID != conversation {
				t.Fatal("question origin lost")
			}
			if !withdrawalChecked {
				// The next request is an event-order barrier: both withdrawals
				// precede it in the worker stream. No sleep can make this pass.
				if withdrawnQuestion == nil || withdrawnQuestion.Card.Outcome == nil || *withdrawnQuestion.Card.Outcome != protocol.DecisionOutcomeStopped {
					t.Fatal("scheduled worker withdrawal left its question pending")
				}
				if withdrawnApproval == nil {
					t.Fatal("scheduled worker approval was never presented")
				}
				assertScheduledWithdrawal(t, h, *withdrawnQuestion, *withdrawnApproval)
				withdrawalChecked = true
			}
			answer := "confirmed"
			values := protocol.FormValues{"answer": protocol.FormValue{String: &answer}}
			if _, err = waiting.AnswerQuestion(answerCtx, protocol.QuestionAnswer{CardID: &card.Card.ID, Revision: &card.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}); err != nil {
				t.Fatal(err)
			}
			answeredQuestions[card.Card.ID] = true
		case protocol.ApprovalRequest:
			if card.Card != nil {
				withdrawn := false
				for _, target := range card.Card.Targets {
					var args struct {
						ItemID string `json:"itemId"`
					}
					if strings.HasSuffix(target.Name, "arguments") && json.Unmarshal([]byte(target.Value), &args) == nil && args.ItemID == "withdrawn" {
						withdrawn = true
					}
				}
				if withdrawn {
					withdrawnApproval = &card
					continue
				}
			}
			if approvalAnswered {
				continue
			}
			if card.Card == nil || card.ConversationID == nil || *card.ConversationID != conversation {
				t.Fatal("approval origin lost")
			}
			if _, err = waiting.AnswerApproval(answerCtx, protocol.ApprovalAnswer{CardID: &card.Card.ID, Revision: &card.Card.Revision, Choice: protocol.ApprovalChoiceOnce}); err != nil {
				t.Fatal(err)
			}
			approvalAnswered = true
		}
	}
	if len(taskIDs) != 3 {
		t.Fatalf("no dispatched graph: %v", taskIDs)
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store := work.New(db)
	for _, id := range taskIDs {
		card, err := store.Get(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT record FROM coding_assignments WHERE id=?", id).Scan(&raw)
		}); err != nil {
			t.Fatal(err)
		}
		var assignment struct {
			Owner, Conversation string
			Delegated           bool
			Origin              struct{ Person, Agent, Conversation, Turn string }
		}
		if err = json.Unmarshal(raw, &assignment); err != nil || !assignment.Delegated || assignment.Owner != card.Task.AgentID || assignment.Conversation != card.Task.ConversationID || assignment.Origin.Conversation != conversation || assignment.Origin.Agent == assignment.Owner || assignment.Origin.Turn != "turn:"+card.Task.Schedule.InputID {
			t.Fatalf("scheduled task lost manager ownership or source delivery: %s %v", raw, err)
		}
	}
	if managerCalls.Load() != 6 {
		t.Fatalf("expected three durable manager judgments, got %d model calls", managerCalls.Load())
	}
	await := func(id string, predicate func(work.Card) bool) work.Card {
		t.Helper()
		deadline := time.NewTimer(operationTimeout)
		defer deadline.Stop()
		retry := time.NewTicker(5 * time.Millisecond)
		defer retry.Stop()
		for {
			c, err := store.Get(h.ctx, id)
			if err == nil && predicate(c) {
				return c
			}
			select {
			case <-deadline.C:
				t.Fatalf("task state not reached: %+v %v", c, err)
			case <-retry.C:
			}
		}
	}
	if outcome != nil {
		checkScheduledOutcomes(t, h, conversation, taskIDs)
		// Persona delivery and scheduler settlement use separate lanes. Wait
		// for both observations before asserting the terminal task projection.
		for _, i := range []int{0, 2} {
			await(taskIDs[i], func(c work.Card) bool { return c.Run != nil && !c.Active })
		}
		checkScheduledOutcomeRecords(t, h, store, conversation, taskIDs, outcome)
		client.Close()
		h.stopCore(core)
		recalling.Store(true)
		core = start()
		client, conversation = conversationClient(t, h)
		admittedTurn(t, h, client, conversation, "recall-scheduled", "Recall both scheduled worker reports.")
		checkScheduledOutcomeRecords(t, h, store, conversation, taskIDs, outcome)
		client.Close()
		h.stopCore(core)
		return
	}
	if stopMode != "" {
		controls, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "stop-fixture", []protocol.Capability{{Name: "running", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer controls.Close()
		control := func(payload protocol.Payload) protocol.Envelope {
			t.Helper()
			id, err := controls.Control(h.ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			for {
				frame, err := controls.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if frame.ReplyTo == nil || *frame.ReplyTo != id {
					continue
				}
				if frame.Error != nil {
					t.Fatalf("scheduled stop failed: %+v", frame.Error)
				}
				return frame
			}
		}
		active, queued := "", []string{}
		for _, id := range taskIDs {
			card, err := store.Get(h.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if card.Active {
				if active != "" {
					t.Fatal("single-slot fixture started two workers")
				}
				active = id
			} else {
				queued = append(queued, id)
			}
		}
		if active == "" || len(queued) != 2 {
			t.Fatal("fixture did not retain queued work")
		}
		wantCount := "3"
		if stopMode == "row_then_all" {
			row := "task:" + queued[0] + ":0"
			control(protocol.Stop{RunID: &row, ConversationID: &conversation})
			await(queued[0], func(c work.Card) bool { return c.Scheduling.Closed && c.State == "stopped" })
			wantCount = "2"
		}
		frame := control(protocol.MultiTaskControl{Action: "stop_all", ConversationID: &conversation})
		result, ok := frame.Payload.(protocol.MultiTaskControl)
		if !ok || result.Count == nil || string(*result.Count) != wantCount {
			t.Fatalf("wrong scheduled stop count: %+v", frame)
		}
		await(active, func(c work.Card) bool { return !c.Active && c.State == "stopped" })
		assertStopped := func() {
			t.Helper()
			for _, id := range taskIDs {
				c, err := store.Get(h.ctx, id)
				if err != nil || !c.Scheduling.Closed || c.State != "stopped" || c.Scheduling.CapacityHeld || c.Scheduling.AreaHeld {
					t.Fatalf("stopped schedule changed: %+v %v", c, err)
				}
				if id != active {
					if _, err := os.Stat(filepath.Join(h.root, id+".definition.json")); !os.IsNotExist(err) {
						t.Fatalf("queued worker launched after stop: %s %v", id, err)
					}
				}
			}
		}
		assertStopped()
		controls.Close()
		client.Close()
		waiting.Close()
		h.stopCore(core)
		core = start()
		client, conversation = conversationClient(t, h)
		admittedTurn(t, h, client, conversation, "stopped-restart", "Refresh the stopped work.")
		assertStopped()
		client.Close()
		h.stopCore(core)
		return
	}
	if fault != nil {
		failedID := ""
		for _, id := range []string{taskIDs[0], taskIDs[2]} {
			card := await(id, func(c work.Card) bool {
				return c.State == "failed" || c.Run != nil && !c.Active && c.TurnStatus == "completed"
			})
			if card.State == "failed" {
				if failedID != "" || card.Run != nil || card.Scheduling.CapacityHeld || card.Scheduling.AreaHeld {
					t.Fatalf("failed dispatch retained reservations: %+v", card)
				}
				failedID = id
			} else if _, err := os.Stat(filepath.Join(h.root, id+".receipt.json")); err != nil {
				t.Fatalf("independent worker did not deliver: %v", err)
			}
		}
		if failedID == "" {
			t.Fatal("fixture did not exercise pre-dispatch failure")
		}
		client.Close()
		h.stopCore(core)
		core = start()
		failed, err := store.Get(h.ctx, failedID)
		if err != nil || failed.State != "failed" || failed.Run != nil || failed.Scheduling.CapacityHeld || failed.Scheduling.AreaHeld {
			t.Fatalf("restart replayed failed dispatch: %+v %v", failed, err)
		}
		h.stopCore(core)
		return
	}
	attempts := 1
	if conflict != nil {
		attempts += conflict.Returns
	}
	a := await(taskIDs[0], func(c work.Card) bool {
		return c.Run != nil && c.Run.Generation == attempts && !c.Active && c.TurnStatus == "completed" &&
			(conflict == nil || c.Scheduling.PRRun == c.Run.ID)
	})
	if conflict != nil {
		if !a.Scheduling.ReturnSent || a.State != "input_needed" {
			t.Fatalf("repeated conflict did not stop after correction: %+v", a)
		}
		admittedTurn(t, h, client, conversation, "conflict-input", "Observe the remaining conflict again.")
	}
	await(taskIDs[2], func(c work.Card) bool { return c.Run != nil && !c.Active })
	assertProtectedRefs := func() {
		t.Helper()
		dir := filepath.Join(h.root, "work", "tasks", taskIDs[0])
		head := runGit("-C", dir, "rev-parse", "HEAD")
		otherRef := "refs/heads/lina/" + taskIDs[2]
		otherHead := runGit("rev-parse", otherRef)
		for _, args := range [][]string{
			{"update-ref", "refs/heads/dev", head},
			{"update-ref", "refs/remotes/origin/dev", head},
			{"update-ref", otherRef, head},
			{"update-ref", "-d", "refs/heads/lina/" + taskIDs[0]},
		} {
			command := exec.CommandContext(h.ctx, git, append([]string{"-C", dir}, args...)...)
			command.Env = []string{"HOME=" + h.root, "PATH=" + filepath.Dir(git), "GIT_CONFIG_NOSYSTEM=1"}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "LINA protected ref") {
				t.Fatalf("scheduled worker bypassed protected refs with git %v: %v %s", args, err, output)
			}
		}
		if runGit("rev-parse", "dev") != initialBase || runGit("rev-parse", otherRef) != otherHead {
			t.Fatal("rejected worker command changed a protected branch")
		}
	}
	assertProtectedRefs()
	b, err := store.Get(h.ctx, taskIDs[1])
	if err != nil || b.Run != nil || b.WaitReason == nil || b.WaitReason.Kind != "dependency" {
		t.Fatalf("premature dependent: %+v %v", b, err)
	}
	if !a.Scheduling.AreaHeld || a.Scheduling.Landed {
		t.Fatal("finish released write lease")
	}
	// The forge fixture reports merged only after the real repository has merged.
	runGit("-C", forge, "fetch", "origin")
	runGit("-C", forge, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "merge", "--no-ff", "origin/lina/"+a.Task.ID, "-m", "land fixture foundation")
	runGit("-C", forge, "push", "origin", "dev")
	if runGit("rev-parse", "dev") != initialBase {
		t.Fatal("forge merge changed parent clone local base")
	}
	if err = os.WriteFile(filepath.Join(h.root, a.Task.ID+".merged"), []byte("observed"), 0600); err != nil {
		t.Fatal(err)
	}
	admittedTurn(t, h, client, conversation, "refresh-input", "Observe the landed foundation.")
	b = await(taskIDs[1], func(c work.Card) bool { return c.Run != nil && !c.Active && c.TurnStatus == "completed" })
	for _, id := range taskIDs {
		dir := filepath.Join(h.root, "work", "tasks", id)
		if _, err = os.Stat(dir); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(h.root, id+".receipt.json")); err != nil {
			t.Fatalf("worker receipt was not acknowledged: %v", err)
		}
		count, err := os.ReadFile(filepath.Join(h.root, id+".started"))
		want := "1"
		if id == taskIDs[0] {
			want = fmt.Sprint(attempts)
		}
		if err != nil || string(count) != want {
			t.Fatalf("duplicate/missing worker %s: %s %v", id, count, err)
		}
	}
	result, err := os.ReadFile(filepath.Join(b.Task.Schedule.Workdir, "shared", "b.txt"))
	if err != nil || string(result) != "verified fixture result\n" {
		t.Fatalf("missing worker result: %q %v", result, err)
	}
	client.Close()
	h.stopCore(core)
	core = start()
	client, _ = conversationClient(t, h)
	after, err := store.Get(h.ctx, taskIDs[0])
	if err != nil || !after.Scheduling.Landed || after.State == "done" {
		t.Fatalf("restart lost merge or forged verdict: %+v %v", after, err)
	}
	if runGit("rev-parse", "dev") != initialBase {
		t.Fatal("scheduler rewrote parent clone local base")
	}
	assertProtectedRefs()
	client.Close()
	h.stopCore(core)
}
