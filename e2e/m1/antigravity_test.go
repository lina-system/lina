// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM3AntigravityThroughInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m3/antigravity.json", true)
}

func testHeadlessWorkerThroughInstalledCore(t *testing.T, scenarioPath string, presetVariants bool) {
	t.Helper()
	raw, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Expected, Preset, Choice string
			LegacyAdmission                bool `json:"legacy_admission"`
			HostCapacity                   bool `json:"host_capacity"`
			Omit                           string
			DesignRef                      string `json:"design_ref"`
			MoveBase                       bool   `json:"move_base"`
			ToolCode                       string `json:"tool_code"`
		}
		What, Done             string
		Worker, Binary, Script string
		Recovery               bool
		CrashRecovery          bool `json:"crash_recovery"`
		WorkArea               bool `json:"work_area"`
		CompletionEvents       bool `json:"completion_events"`
		Definition             bool `json:"definition"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.Worker == "" {
		scenario.Worker, scenario.Binary, scenario.Script = "antigravity", "agy", "../m3/antigravity-worker.mjs"
	}
	for _, preset := range []string{"system", "creative"} {
		if !presetVariants {
			break
		}
		for _, test := range scenario.Cases {
			if test.Name == "success" {
				test.Name += "-" + preset
				test.Preset = preset
				scenario.Cases = append(scenario.Cases, test)
				break
			}
		}
	}
	for _, test := range scenario.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			workerCase := test.Name
			if test.Preset != "" || test.LegacyAdmission || test.MoveBase || test.HostCapacity {
				workerCase = "success"
			}
			var err error
			fixture := t.TempDir()
			load := newWorkerLoad(t, fixture)
			repo := filepath.Join(fixture, "repo")
			bin := filepath.Join(fixture, "bin")
			for _, dir := range []string{repo, bin} {
				if err = os.MkdirAll(dir, 0700); err != nil {
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
			os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\nProduce result.txt.\n\n```md\n# Example\n```\n"), 0600)
			git("add", "design.md")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
			base := git("rev-parse", "dev")
			git("remote", "add", "origin", "https://github.com/fixture/project.git")
			script, err := os.ReadFile(scenario.Script)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{scenario.Binary, "gh"} {
				if err = os.WriteFile(filepath.Join(bin, name), script, 0700); err != nil {
					t.Fatal(err)
				}
			}
			codexScript, err := os.ReadFile("../m2/worker-choice-codex.mjs")
			if err != nil {
				t.Fatal(err)
			}
			if scenario.CrashRecovery {
				artifact, err := os.ReadFile("../m3/antigravity-worker.mjs")
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(bin, "artifact-worker"), artifact, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.Binary != "codex" {
				if err = os.WriteFile(filepath.Join(bin, "codex"), codexScript, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var calls, providerCalls atomic.Int32
			var completionHarness *harness
			var offered atomic.Bool
			var moved atomic.Bool
			var sourceBase atomic.Value
			sourceBase.Store(base)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls.Add(1)
				var req struct {
					Tools []struct{ Name string }
					Input []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				// Move the branch before returning any manager response, so
				// execution cannot race this fixture's admission boundary.
				for _, tool := range req.Tools {
					if tool.Name == "coding_decision" && test.MoveBase && moved.CompareAndSwap(false, true) {
						if err := os.WriteFile(filepath.Join(repo, "design.md"), []byte("# New design\n"), 0600); err != nil {
							t.Error(err)
							return
						}
						git("add", "design.md")
						git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Move base after validation")
						sourceBase.Store(git("rev-parse", "dev"))
					}
				}
				if scenario.CompletionEvents && completionHarness != nil {
					raw, _ := json.Marshal(req.Input)
					if strings.Contains(string(raw), "coding_delivery") {
						assertClaimBeforeManager(t, completionHarness)
					}
				}
				if scriptedCodingManager(t, w, req) {
					return
				}
				callNumber := calls.Add(1)
				if callNumber == 1 || test.Choice == "overlap" && callNumber == 3 {
					for _, tool := range req.Tools {
						if tool.Name == "dispatch_work" {
							offered.Store(true)
						}
					}
					if !offered.Load() {
						t.Error("running Core does not offer dispatch_work with installed worker")
						emitResponse(w, "Unavailable.", true)
						return
					}
					args := map[string]any{"repository": repo, "base": "dev", "branch": "task-branch", "what": scenario.What, "why": "Prove real worker execution", "scope": []string{"result.txt"}, "not_touch": []string{"design.md"}, "done": scenario.Done, "design_refs": []string{"design.md#result"}, "write_area": []string{"result.txt"}}
					if test.Omit != "" {
						delete(args, test.Omit)
					}
					if test.DesignRef != "" {
						args["design_refs"] = []string{test.DesignRef}
					}
					if scenario.Worker == "claude" {
						args["worker"] = scenario.Worker
					}
					emitPersonaCall(w, "worker-start", "dispatch_work", args)
				} else {
					code := test.ToolCode
					if code == "" {
						code = "ok"
					}
					if !test.HostCapacity || callNumber == 2 {
						methodologyOutput(t, methodologyRequest{Input: req.Input}, code, nil)
					}
					emitResponse(w, "Task accepted.", true)
				}
			}))
			defer server.Close()
			h, bootstrap := installedConversationHarness(t, server.URL)
			ids := seedCodingFixture(t, h, bootstrap, test.Preset)
			if test.Choice == "rule" || test.Choice == "quota-windows" {
				table := map[string]any{"provisional": true, "rules": []any{map[string]any{"worker": "codex", "tags": []string{repo}, "priority": 10}}}
				if err = os.WriteFile(filepath.Join(h.root, "system", "core", "worker-choice.json"), mustJSON(t, table), 0600); err != nil {
					t.Fatal(err)
				}
			}
			completionHarness = h
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			t.Cleanup(cancel)
			h.ctx = ctx
			managerTurns := 2
			var checkLegacy func()
			if test.LegacyAdmission {
				checkLegacy = seedLegacyDispatch(t, h, ids["dev"], repo, scenario.What, scenario.Done)
				// The old manager decision is seeded; only delivery runs here.
				managerTurns = 1
			}
			fixtureCase := workerCase
			if test.HostCapacity {
				load.set(t, "1000000.00")
			}
			if test.Choice != "" {
				fixtureCase = test.Choice
			}
			start := func() *process {
				p := h.command("core", "--state-root", h.root, "--socket", h.socket)
				p.command.Env = append(p.command.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "LINA_WORKER_FIXTURE="+fixture, "LINA_WORKER_CASE="+fixtureCase, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
				load.wrap(t, p)
				p.start(t)
				h.connectWithin(p, time.Minute).Close()
				waitCoreReady(t, h)
				return p
			}
			core := start()
			t.Cleanup(func() {
				if t.Failed() {
					logWorkerDispatchState(t, h, fixture)
				}
				if t.Failed() && test.Choice == "quota-windows" {
					logWorkerQuotaState(t, h)
				}
			})
			if test.LegacyAdmission {
				if _, err = os.Stat(filepath.Join(fixture, "workdir")); !os.IsNotExist(err) {
					t.Fatal("legacy admission ran before its conversation receipt", err)
				}
			}
			c, conversation := conversationClient(t, h)
			events := admittedTurn(t, h, c, conversation, "delegate", "Create result.txt through the installed worker and submit its PR.")
			if !offered.Load() || t.Failed() {
				c.Close()
				h.stopCore(core)
				return
			}
			if scenario.CrashRecovery {
				waitDispatchedRecovery(t, h, func(rec dispatch.Record) bool {
					if message, err := os.ReadFile(filepath.Join(fixture, "worker-error")); err == nil {
						t.Fatalf("worker fixture: %s", message)
					}
					_, err := os.Stat(filepath.Join(fixture, "worker-ready"))
					return err == nil && rec.Thread != nil
				})
				c.Close()
				core.killAndWait()
				core = start()
				waitDispatchedRecovery(t, h, func(rec dispatch.Record) bool {
					_, err := os.Stat(filepath.Join(fixture, "snapshot-requested"))
					return err == nil && rec.State == "unknown"
				})
				if err = os.WriteFile(filepath.Join(fixture, "snapshot-release"), []byte("read"), 0600); err != nil {
					t.Fatal(err)
				}
				c, conversation = conversationClient(t, h)
				events = nil
			}
			if test.ToolCode == "invalid_arguments" {
				c.Close()
				h.stopCore(core)
				assertDefinitionRefused(t, h, fixture)
				return
			}
			if test.Choice == "overlap" {
				admittedTurn(t, h, c, conversation, "delegate-overlap", "Create another result in the same write area.")
				waitForAreaClaim(t, h, fixture)
				if err = os.WriteFile(filepath.Join(fixture, "release-first"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				managerTurns++
			}
			var capacityID string
			resultContext := h.ctx
			if test.HostCapacity {
				capacityID = waitForHostCapacity(t, h, fixture)
				load.set(t, "0.00")
				// Finish before the dispatcher's minute-long safety sweep: the
				// new input's committed event must wake this queued dispatch.
				var cancel context.CancelFunc
				resultContext, cancel = context.WithTimeout(h.ctx, operationTimeout)
				defer cancel()
				recovery := *h
				recovery.ctx = resultContext
				events = append(events, admittedTurn(t, &recovery, c, conversation, "capacity-released", "The host has capacity again.")...)
			}
			found := false
			inspect := func(e protocol.Envelope) {
				if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil && (strings.Contains(*item.Text, scenario.Worker+":") || test.Choice != "" && strings.Contains(*item.Text, "codex:")) {
					if !strings.Contains(*item.Text, test.Expected) {
						t.Fatalf("unexpected worker result: %s", *item.Text)
					}
					found = true
				}
			}
			for _, e := range events {
				inspect(e)
			}
			for !found {
				e, err := c.Receive(resultContext)
				if err != nil {
					t.Fatal("worker result missing", err)
				}
				inspect(e)
			}
			if scenario.CompletionEvents {
				waitCompletionHandled(t, h)
			}
			if scenario.CrashRecovery {
				waitDispatchedRecovery(t, h, func(rec dispatch.Record) bool {
					return rec.State == "finished" && rec.Announced && rec.Snapshot != nil && rec.Attempt == 1 && rec.Recoveries == 0
				})
			}
			c.Close()
			h.stopCore(core)
			assertCodingOwnership(t, h, ids["dev"], conversation, workerCase == "success", managerTurns)
			if test.Choice != "" {
				assertWorkerChoice(t, h, fixture, test.Choice)
			}
			if scenario.Worker == "claude" {
				assertClaudeReceipt(t, h, workerCase)
				if scenario.Recovery {
					assertHeadlessRecovery(t, h)
				}
			}

			if scenario.CompletionEvents {
				assertWorkCompletion(t, h)
			}
			if checkLegacy != nil {
				checkLegacy()
			}
			if git("rev-parse", "dev") != sourceBase.Load().(string) {

				t.Fatal("worker changed protected base")
			}
			if workerCase == "success" || workerCase == "pending" {
				wd, err := os.ReadFile(filepath.Join(fixture, "workdir"))
				if err != nil {
					t.Fatal(err)
				}
				if scenario.Definition {
					assertDefinitionPacket(t, h, fixture, string(wd), base)
				}
				if scenario.WorkArea {
					assertDispatchedWorkArea(t, h, fixture, repo, string(wd), base)
				}
				body, err := os.ReadFile(filepath.Join(string(wd), "result.txt"))
				if err != nil || !strings.Contains(string(body), "worker produced") {
					t.Fatal("no real worker artifact", err)
				}
				if _, err = os.Stat(filepath.Join(fixture, "created")); err != nil {
					t.Fatal("worker did not open PR")
				}
			}
			beforeRestart := providerCalls.Load()
			if test.HostCapacity {
				assertCapacityWorkOnce(t, h, fixture, capacityID)
			}
			if scenario.CompletionEvents {
				loseCompletion(t, h)
			}
			core = start()
			c, _ = conversationClient(t, h)
			topic := "conversation:" + conversation
			if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			e, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := json.Marshal(e.Payload)
			if !strings.Contains(string(saved), test.Expected) {
				t.Fatalf("durable result lost: %s", saved)
			}
			c.Close()
			h.stopCore(core)
			if scenario.Recovery {
				count, err := os.ReadFile(filepath.Join(fixture, "attempt-count"))
				if err != nil || string(count) != "2" {
					t.Fatalf("restart repeated a headless attempt: %s %v", count, err)
				}
			}
			if scenario.CompletionEvents {
				assertWorkCompletion(t, h)
			}
			if (scenario.CompletionEvents || test.HostCapacity) && providerCalls.Load() != beforeRestart {
				t.Fatal("restart replayed a manager or conversation model call")
			}
			expectedCalls := int32(2)
			if test.HostCapacity {
				expectedCalls++
				assertCapacityWorkOnce(t, h, fixture, capacityID)
			}
			if test.Choice == "overlap" {
				expectedCalls = 4
			}
			if calls.Load() != expectedCalls {
				t.Fatalf("restart replayed provider: %d", calls.Load())
			}
			if test.Choice != "" {
				assertWorkerChoice(t, h, fixture, test.Choice)
			}
			if test.LegacyAdmission {
				launches, err := os.ReadFile(filepath.Join(fixture, "launches"))
				if err != nil || string(launches) != "worker\n" {
					t.Fatalf("legacy admission did not execute exactly once: %q %v", launches, err)
				}
			}
		})
	}
}
