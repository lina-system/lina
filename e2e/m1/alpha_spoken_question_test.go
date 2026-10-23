// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Hold the handoff and then the worker-result response at explicit provider
// boundaries. The acceptance observer must not answer either unfinished turn.
func TestAlphaSpokenQuestionInstalledCore(t *testing.T) {
	t.Parallel()
	const request = "Ask the worker which name to use and tell me when it asks."
	const handoff = "The worker will ask which name to use."
	const workerQuestion = "What name should I use?"
	const reply = "코딩 작업에 사용할 이름을 알려 주세요."
	for _, screen := range []string{request, request + "\n" + handoff, workerQuestion} {
		if alphaQuestionVisible(reply, screen) {
			t.Errorf("screen without LINA's question accepted: %q", screen)
		}
	}
	workdir := t.TempDir()
	ack, finish := make(chan struct{}), make(chan struct{})
	workerRelease := make(chan struct{})
	partial := make(chan struct{})
	var ackOnce, finishOnce, partialOnce, workerOnce sync.Once
	releaseAck := func() { ackOnce.Do(func() { close(ack) }) }
	releaseFinish := func() { finishOnce.Do(func() { close(finish) }) }
	releaseWorker := func() { workerOnce.Do(func() { close(workerRelease) }) }
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker-finish" {
			select {
			case <-workerRelease:
			case <-r.Context().Done():
			}
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
		var last struct{ Type, Content, Output string }
		if err := json.Unmarshal(req.Input[len(req.Input)-1], &last); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(workerCompletionInput(req.Input), workerQuestion) {
			t.Log("provider received the worker question")
			emitResponse(w, reply, false)
			w.(http.Flusher).Flush()
			partialOnce.Do(func() { close(partial) })
			select {
			case <-finish:
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			case <-r.Context().Done():
			}
			return
		}
		if last.Type == "function_call_output" {
			select {
			case <-ack:
				emitResponse(w, handoff, true)
			case <-r.Context().Done():
			}
			return
		}
		emitPersonaCall(w, "start-worker", "worker_start", map[string]any{"workdir": workdir, "task_packet": "Ask the person for a name before continuing."})
	})))
	defer server.Close()
	defer releaseAck()
	defer releaseFinish()
	defer releaseWorker()
	h, bootstrap := installedConversationHarness(t, server.URL)
	seedCodingFixture(t, h, bootstrap)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_WORKER_FINISH_GATE="+server.URL+"/worker-finish", "LINA_WORKER_FINISH_QUESTION="+workerQuestion, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	defer core.killAndWait()
	h.connect(core).Close()
	waitCoreReady(t, h)
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "turn_activity", Version: "1.0"})
	defer c.Close()
	key, text := "delegate", request
	if _, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal("worker start did not arrive:", err)
		}
		if activity, ok := frame.Payload.(protocol.ToolActivity); ok && activity.CallID == "start-worker" && activity.Status == protocol.ToolActivityStatusFinished {
			if activity.Outcome == nil || *activity.Outcome != protocol.ToolActivityOutcomeOk {
				t.Fatalf("worker start failed: %+v", activity)
			}
			break
		}
	}
	releaseWorker()
	waitWorkerCanon(t, h, `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'`, 1)
	assertPending := func(stage string) {
		t.Helper()
		if got := alphaSpokenQuestion(t, h, conversation); got != "" {
			t.Errorf("%s accepted as delivered worker question: %q", stage, got)
		}
	}
	assertPending("original request")
	releaseAck()
	// The unrelated completed turn is the initial delegation, not the response
	// to the worker's question already stored in canon.
	receiveWorkerFollowups(t, h, c, 1)
	assertPending("handoff acknowledgement")
	select {
	case <-partial:
	case <-h.ctx.Done():
		t.Fatal("worker-result response did not start")
	}
	assertPending("incomplete response")
	releaseFinish()
	receiveWorkerFollowups(t, h, c, 1)
	if got := alphaSpokenQuestion(t, h, conversation); got != reply {
		t.Errorf("committed worker response=%q, want %q", got, reply)
	}
	if got := alphaSpokenQuestion(t, h, "other-conversation"); got != "" {
		t.Errorf("another conversation accepted worker question: %q", got)
	}
}

func TestAlphaSpokenQuestionScreen(t *testing.T) {
	const reply = "코딩 작업에 사용할 **이름**을 알려 주세요."
	for _, scenario := range []struct {
		name, reply, screen string
		want                bool
	}{
		{"original-request", reply, "Ask the worker which name to use.", false},
		{"handoff", reply, "The worker will ask which name to use.", false},
		{"worker-output", reply, "What name should I use?", false},
		{"empty-reply", "", "What name should I use?", false},
		{"not-a-question", "작업을 시작했어요.", "What name should I use?", false},
		{"rendered-question", reply, "코딩 작업에 사용할 이름을 알려 주세요.", true},
		{"wrapped-question", reply, "코딩 작업에 사용할 이름을\n    알려 주세요.", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := alphaQuestionVisible(scenario.reply, scenario.screen); got != scenario.want {
				t.Fatalf("visible=%t, want %t", got, scenario.want)
			}
		})
	}
}
