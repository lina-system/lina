// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestM2InstalledCodeGoalTool(t *testing.T) {
	t.Parallel()
	var offered atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Tools []struct{ Name string } }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, tool := range request.Tools {
			if tool.Name == "dispatch_code_goal" {
				offered.Store(true)
			}
		}
		emitResponse(w, "Ready.", true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "goal", "Run this code goal through verification.")
	c.Close()
	h.stopCore(core)
	if !offered.Load() {
		t.Fatal("installed Core does not supply the code-goal Driver or dispatch_code_goal")
	}
}

func TestM2InstalledCodeGoalDriver(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"private", "public", "failed-deploy", "restart-deploy", "restart-unhealthy-probe", "external-merge", "no-antigravity", "completion-evidence", "completion-missing", "completion-wrong-kind"} {
		t.Run(mode, func(t *testing.T) { t.Parallel(); installedCodeGoal(t, mode) })
	}
}

func installedCodeGoal(t *testing.T, mode string) {
	fixture := t.TempDir()
	repo := filepath.Join(fixture, "repo")
	bin := filepath.Join(fixture, "bin")
	for _, dir := range []string{repo, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + fixture, "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	if err := os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\nProduce result.txt.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "design.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
	base := git("rev-parse", "HEAD")
	git("remote", "add", "origin", "https://github.com/fixture/project.git")
	worker, err := os.ReadFile("../m3/antigravity-worker.mjs")
	if err != nil {
		t.Fatal(err)
	}
	services, err := os.ReadFile("../m2/code-goal-services.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, "agy"), worker, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh", "qa", "acceptance", "deploy", "probe", "host-report"} {
		if err = os.WriteFile(filepath.Join(bin, name), services, 0700); err != nil {
			t.Fatal(err)
		}
	}
	workerPath := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if mode == "no-antigravity" {
		if err = os.Remove(filepath.Join(bin, "agy")); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"node", "git", "sh"} {
			binary, err := exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(binary, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		workerPath = bin
	}
	var mu sync.Mutex
	deployed := map[string]string{}
	restarting := strings.HasPrefix(mode, "restart-")
	var failedProbes atomic.Int32
	deployedSignal := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	if !restarting {
		close(releaseProbe)
	}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			select {
			case <-releaseProbe:
			case <-r.Context().Done():
				return
			}
			if mode == "restart-unhealthy-probe" && failedProbes.CompareAndSwap(0, 1) {
				// A valid negative probe does not settle the interrupted deploy.
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"body":"starting"}`))
				return
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&deployed); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			select {
			case deployedSignal <- struct{}{}:
			default:
			}
		}
		_ = json.NewEncoder(w).Encode(deployed)
	}))
	defer service.Close()
	defer func() {
		select {
		case <-releaseProbe:
		default:
			close(releaseProbe)
		}
	}()
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request methodologyRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var completion *result.CompletionRequest
		if mode == "completion-evidence" || mode == "completion-wrong-kind" {
			kind := "owner_path_run"
			if mode == "completion-wrong-kind" {
				kind = "labelled_claim"
			}
			completion = &result.CompletionRequest{WorkKind: "code", Evidence: []result.EvidenceReference{{Kind: kind, CallID: "owner-path-observation"}}}
		}
		if scriptedCodingManager(t, w, request, completion) {
			return
		}
		if calls.Add(1) == 1 {
			offered := false
			for _, tool := range request.Tools {
				offered = offered || tool.Name == "dispatch_code_goal"
			}
			if !offered {
				t.Error("installed Core has no goal driver")
				emitResponse(w, "Unavailable.", true)
				return
			}
			emitPersonaCall(w, "code-goal", "dispatch_code_goal", map[string]any{"outcome": "Produce and run the result", "done": "The running service returns the result", "scope": map[string]any{"repositories": []string{"fixture/project"}, "includes": "result.txt", "excludes": "design.md"}, "criteria": []any{map[string]any{"id": "result", "statement": "The result matches the request", "check": map[string]any{"kind": "check", "method": "acceptance", "target": "fixture/project"}}}, "service_target": "fixture-service", "tasks": []any{map[string]any{"id": "change", "done": "Produce result.txt", "repository": "fixture/project", "base": "dev", "area": "result.txt", "worker": "antigravity", "resources": map[string]any{"cpu": 0.01, "memory_mb": 1, "disk_mb": 1}}}})
		} else {
			methodologyOutput(t, request, "ok", nil)
			emitResponse(w, "Work started.", true)
		}
	}))
	defer model.Close()
	h, bootstrap := installedConversationHarness(t, model.URL)
	seedCodingFixture(t, h, bootstrap)
	command := func(name string) map[string]any { return map[string]any{"path": filepath.Join(bin, name)} }
	config := map[string]any{"host_report": command("host-report"), "limits": map[string]any{"cpu": 1, "memory_mb": 4096, "disk_mb": 16384}, "repositories": map[string]any{"fixture/project": map[string]any{"clone": repo, "base": "dev", "design_refs": []string{"design.md#result"}, "qa": command("qa"), "checks": map[string]any{"acceptance": command("acceptance")}, "service": map[string]any{"target": "fixture-service", "public": mode == "public", "deploy": command("deploy"), "probe": command("probe")}}}}
	raw, _ := json.Marshal(config)
	if err = os.WriteFile(filepath.Join(h.root, "system", "code-work.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	h.ctx = ctx
	start := func() *process {
		operation := h.ctx
		h.ctx = t.Context()
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		h.ctx = operation
		p.command.Env = append(p.command.Env, "PATH="+workerPath, "LINA_WORKER_FIXTURE="+fixture, "LINA_WORKER_CASE=success", "LINA_GOAL_FIXTURE_URL="+service.URL, "LINA_GOAL_MODE="+mode, "LINA_RESPONSES_BASE_URL="+model.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connectWithin(p, time.Minute).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	c, conversation := conversationClient(t, h)
	events := admittedTurn(t, h, c, conversation, "goal", "Create the result and run it through deployment and verification.")
	if mode == "no-antigravity" {
		waitCodeGoalState(t, h, func(tx *canon.Tx) (bool, error) {
			var raw []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT body FROM work_records WHERE kind='goal'").Scan(&raw); err != nil {
				return false, err
			}
			var g goal.Goal
			if err := json.Unmarshal(raw, &g); err != nil {
				return false, err
			}
			if len(g.Waits) != 1 || g.Waits[0].Reason != "host_capacity" || g.Waits[0].Wake != "node.changed" {
				return false, nil
			}
			if err := tx.QueryRowContext(h.ctx, "SELECT body FROM work_records WHERE kind='node'").Scan(&raw); err != nil {
				return false, err
			}
			var host goal.Node
			if err := json.Unmarshal(raw, &host); err != nil {
				return false, err
			}
			if !host.Connected || host.Workers["antigravity"] {
				t.Fatalf("unavailable Antigravity reported ready: %+v", host)
			}
			var count int
			if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM worker_dispatch").Scan(&count); err != nil {
				return false, err
			}
			if count != 0 || g.Tasks[0].Stage != goal.TaskReady || g.Tasks[0].Attempt != 0 {
				t.Fatalf("unavailable worker was dispatched: count=%d goal=%+v", count, g)
			}
			return true, nil
		})
		c.Close()
		h.stopCore(core)
		if _, err := os.Stat(filepath.Join(fixture, "allocation.json")); !os.IsNotExist(err) {
			t.Fatal("unavailable worker allocated a workspace", err)
		}
		return
	}
	if mode == "external-merge" {
		waitCodeGoalState(t, h, func(tx *canon.Tx) (bool, error) {
			var state string
			err := tx.QueryRowContext(h.ctx, "SELECT json_extract(body,'$.state') FROM work_records WHERE kind='action' AND json_extract(body,'$.kind')='merge'").Scan(&state)
			if err != nil || state != "unknown" {
				return false, err
			}
			var bindings int
			if err = tx.QueryRowContext(h.ctx, "SELECT count(*) FROM authority_merges").Scan(&bindings); err != nil {
				return false, err
			}
			if bindings != 0 {
				t.Fatal("fixture merged after the authority binding was already created")
			}
			return true, nil
		})
		c.Close()
		h.stopCore(core)
		core = start()
		c, conversation = conversationClient(t, h)
		events = nil
	}
	if restarting {
		select {
		case <-deployedSignal:
		case <-h.ctx.Done():
			t.Fatal("deployment did not reach the service")
		}
		c.Close()
		h.stopCore(core)
		core = start()
		c, conversation = conversationClient(t, h)
		close(releaseProbe)
		events = nil
		if mode == "restart-unhealthy-probe" {
			waitUnconfirmedCodeDeployment(t, h)
			c.Close()
			h.stopCore(core)
			core = start()
			c, conversation = conversationClient(t, h)
		}
	}
	done := false
	inspect := func(e protocol.Envelope) {
		if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil {
			done = done || strings.HasPrefix(*item.Text, "Done.")
			if strings.HasPrefix(*item.Text, "This work needs attention.") {
				if mode != "public" && mode != "failed-deploy" && mode != "worker-recovery-failed" {
					t.Error("goal blocked: " + *item.Text)
				}
				done = true
			}
		}
	}
	for _, e := range events {
		inspect(e)
	}
	for !done && !t.Failed() {
		e, err := c.Receive(h.ctx)
		if err != nil {
			t.Error("goal result missing: ", err)
			break
		}
		inspect(e)
	}
	c.Close()
	h.ctx = t.Context()
	h.stopCore(core)
	db, closeDB := installedAcceptanceStore(t, h)
	var g goal.Goal
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM work_records WHERE kind='goal'").Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &g)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(mode, "worker-recovery") {
		var record dispatch.Record
		var calls int
		err = db.Read(t.Context(), func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(t.Context(), "SELECT record FROM worker_dispatch").Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM turn_calls WHERE json_extract(call,'$.Name')='dispatch_work'").Scan(&calls)
		})
		launches, readErr := os.ReadFile(filepath.Join(fixture, "launches"))
		if err != nil || readErr != nil || !record.Goal || record.Recoveries != 1 || record.Attempt != 2 || record.ReturnReceipt != "" || calls != 0 || strings.Count(string(launches), "worker\n") != 2 {
			t.Fatalf("goal recovery did not run exactly once without a conversation dispatch: record=%+v calls=%d launches=%s err=%v read=%v", record, calls, launches, err, readErr)
		}
		if mode == "worker-recovery-failed" {
			closeDB()
			if record.State != "decision" || record.DecisionAt == 0 || g.State != goal.GoalBlocked {
				t.Fatalf("second failure did not stop for a decision: dispatch=%+v goal=%+v", record, g)
			}
			core = start()
			h.stopCore(core)
			after, err := os.ReadFile(filepath.Join(fixture, "launches"))
			if err != nil || string(after) != string(launches) {
				t.Fatalf("restart launched a third attempt: %s %v", after, err)
			}
			return
		}
	}
	if mode == "failed-deploy" {
		closeDB()
		if g.State != goal.GoalBlocked || g.Deployment.Passed || g.Verification.Passed {
			t.Fatalf("failed deployment completed the goal: %+v", g)
		}
		events, err := os.ReadFile(filepath.Join(fixture, "events"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(events), "deploy\n") != 1 || strings.Contains(string(events), "probe\n") {
			t.Fatalf("failed deployment retried or accepted a probe: %s", events)
		}
		return
	}
	if mode == "public" {
		closeDB()
		mu.Lock()
		writes := len(deployed)
		mu.Unlock()
		if g.State != goal.GoalBlocked || g.Deployment.Passed || writes != 0 {
			t.Fatalf("public deployment bypassed approval: %+v", g)
		}
		events, err := os.ReadFile(filepath.Join(fixture, "events"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(events), "deploy\n") {
			t.Fatal("unapproved deployment command ran")
		}
		return
	}
	if g.State != goal.GoalDone || len(g.Checks) != 5 || !g.Deployment.Passed || !g.Verification.Passed {
		var debug []string
		_ = db.Read(t.Context(), func(tx *canon.Tx) error {
			rows, e := tx.QueryContext(t.Context(), "SELECT body FROM work_records WHERE kind IN ('action','node')")
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var body string
				_ = rows.Scan(&body)
				debug = append(debug, body)
			}
			return rows.Err()
		})
		t.Fatalf("goal not closed with evidence: %+v\nactions: %v\ncore: %s", g, debug, core.stderr.String())
	}
	if restarting {
		var state, active string
		err = db.Read(t.Context(), func(tx *canon.Tx) error {
			if err := tx.QueryRowContext(t.Context(), "SELECT json_extract(record,'$.State') FROM authority_effects WHERE json_extract(record,'$.Tool')='deploy_code_goal'").Scan(&state); err != nil {
				return err
			}
			return tx.QueryRowContext(t.Context(), "SELECT active_key FROM authority_targets WHERE target='code-deploy:fixture-service'").Scan(&active)
		})
		if err != nil || state != "applied" || active != "" {
			t.Fatalf("reconciled deployment kept authority reserved: state=%s active=%s err=%v", state, active, err)
		}
	}
	if mode == "external-merge" {
		var state, repository, head string
		err = db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT state, repository, json_extract(record,'$.Result.Head') FROM authority_merges").Scan(&state, &repository, &head)
		})
		if err != nil || state != "merged" || repository != "fixture/project" || head != g.Tasks[0].Head {
			t.Fatalf("external merge binding not reconciled: state=%s repository=%s head=%s err=%v", state, repository, head, err)
		}
	}
	closeDB()
	for _, check := range g.Checks {
		if !check.Passed || check.Ref == "" {
			t.Fatalf("missing criterion evidence: %+v", check)
		}
	}
	merge, err := os.ReadFile(filepath.Join(fixture, "merged"))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	revision, body := deployed["revision"], deployed["body"]
	mu.Unlock()
	if revision != string(merge) || body != "worker produced this result\n" || git("rev-parse", "dev") != base {
		t.Fatal("deployment or protected base mismatch")
	}
	before, err := os.ReadFile(filepath.Join(fixture, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if restarting && strings.Count(string(before), "deploy\n") != 1 {
		t.Fatalf("reconciliation repeated deployment: %s", before)
	}
	if mode == "restart-unhealthy-probe" && (failedProbes.Load() != 1 || strings.Count(string(before), "probe\n") < 2) {
		t.Fatalf("failed probe was not followed by a successful observation: %s", before)
	}
	mergeEvent := "merge"
	if mode == "external-merge" {
		mergeEvent = "external-merge"
		if strings.Contains("\n"+string(before), "\nmerge\n") {
			t.Fatalf("observation repeated merge: %s", before)
		}
	}
	for _, name := range []string{"worker-pr", mergeEvent, "qa", "deploy", "probe", "acceptance"} {
		if !strings.Contains(string(before), name+"\n") {
			t.Fatalf("stage %s never ran: %s", name, before)
		}
	}
	core = start()
	h.stopCore(core)
	after, err := os.ReadFile(filepath.Join(fixture, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || calls.Load() != 2 {
		t.Fatalf("restart replayed work: %s -> %s; calls=%d", before, after, calls.Load())
	}
	if strings.HasPrefix(mode, "completion-") {
		if strings.Count(string(after), "owner-path-check\n") != 1 {
			t.Fatalf("owner path check missing or rerun: %s", after)
		}
		db, closeDB := installedAcceptanceStore(t, h)
		defer closeDB()
		var card work.Card
		var breaches int
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(t.Context(), "SELECT record FROM work_cards WHERE json_extract(record,'$.applied')=1").Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &card); err != nil {
				return err
			}
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM done_completions WHERE state='claimed'").Scan(&breaches)
		})
		want, count := "claimed", 1
		if mode == "completion-evidence" {
			want, count = "done", 0
		}
		if err != nil || card.Completion == nil || card.Completion.State != want || card.Completion.PolicyRevision == "" || breaches != count {
			t.Fatalf("Git completion after restart: %+v breaches=%d err=%v", card.Completion, breaches, err)
		}
		if mode == "completion-evidence" && (len(card.Completion.Evidence) != 1 || card.Completion.Evidence[0].TurnID == "" || card.Completion.Evidence[0].CallID != "owner-path-observation") {
			t.Fatalf("lost completion chain: %+v", card.Completion)
		}
	}
}

// Wait for the negative observation to commit before restarting again. This
// exercises durable recovery without waiting for the five-minute safety sweep.
func waitUnconfirmedCodeDeployment(t *testing.T, h *harness) {
	t.Helper()
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var action goal.Action
		var effect, active string
		err = reader.Read(h.ctx, func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT body FROM work_records WHERE kind='action' AND json_extract(body,'$.kind')='deploy'").Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &action); err != nil {
				return err
			}
			if err := tx.QueryRowContext(h.ctx, "SELECT json_extract(record,'$.State') FROM authority_effects WHERE json_extract(record,'$.Tool')='deploy_code_goal'").Scan(&effect); err != nil {
				return err
			}
			return tx.QueryRowContext(h.ctx, "SELECT active_key FROM authority_targets WHERE target='code-deploy:fixture-service'").Scan(&active)
		})
		if err != nil {
			t.Fatal(err)
		}
		if action.ObserverEpoch != 0 {
			var observation goal.Observation
			if err := json.Unmarshal(action.Receipt, &observation); err != nil {
				t.Fatal(err)
			}
			if action.State != "unknown" || observation.Status != goal.OutcomeUnknown || observation.EffectsSettled || observation.Proof.Passed || observation.Proof.Ref == "" || effect != "unknown" || active != action.ID {
				t.Fatalf("negative deployment probe lost reconciliation: action=%s observation=%+v effect=%s active=%s", action.State, observation, effect, active)
			}
			return
		}
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatal("negative deployment probe was not recorded: ", h.ctx.Err())
		}
	}
}

// Observe committed predicates, never a sleep as evidence of progress.
func waitCodeGoalState(t *testing.T, h *harness, ready func(*canon.Tx) (bool, error)) {
	t.Helper()
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var done bool
		err := reader.Read(h.ctx, func(tx *canon.Tx) error { var err error; done, err = ready(tx); return err })
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		if done {
			return
		}
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatal("code goal predicate not reached: ", h.ctx.Err())
		}
	}
}
