// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func alphaOwnerHarness(t *testing.T, worker bool) (*harness, func() *process) {
	t.Helper()
	requireRealModel(t)
	if os.Getenv("LINA_RESPONSES_MODEL") != "claude-opus-5-5" {
		t.Fatal("alpha acceptance requires the owner-selected model")
	}
	h, _ := installedConversationHarness(t, "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		if worker {
			core.command.Env = os.Environ()
		}
		for _, name := range []string{"LINA_RESPONSES_BASE_URL", "LINA_RESPONSES_MODEL", "LINA_RESPONSES_API_KEY"} {
			core.command.Env = append(core.command.Env, name+"="+os.Getenv(name))
		}
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		return core
	}
	return h, start
}

func TestAlphaOwnerKoreanRecallAndSecrets(t *testing.T) {
	h, start := alphaOwnerHarness(t, false)
	core := start()
	c, _ := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, "")
	const fact = "내 시험용 텃밭 이름은 동백정원이고, 토마토 품종은 황금방울이야. 기억해 둬."
	alphaOwnerTurn(t, h, terminal, c, fact, "korean-chat", "")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	core = start()
	c, _ = conversationClient(t, h)
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	alphaScreen(t, h, terminal, "history-restored", func(s string) bool { return strings.Contains(s, "동백정원") }, "")
	recalled := alphaOwnerTurn(t, h, terminal, c, "내 텃밭 이름과 토마토 품종을 다시 알려 줘.", "recalled", "")
	if !strings.Contains(recalled, "동백정원") || !strings.Contains(recalled, "황금방울") {
		t.Error("real model did not recall both facts after Core restart")
	}
	token := "ghp_" + strings.Repeat("AlphaOwnerCanary", 3)
	input := "이 값은 시험용 비밀키야. 저장하거나 되풀이하지 말아 줘: " + token + ". 알겠다고만 답해 줘."
	h.keys(terminal, "\x1b[200~"+input+"\x1b[201~")
	alphaScreen(t, h, terminal, "secret-pasted", func(s string) bool { return strings.Contains(s, "알겠다고만 답해 줘.") }, token)
	h.keys(terminal, "\r")
	alphaOwnerReply(t, h, terminal, c, "secret-reply", token)
	alphaOwnerTurn(t, h, terminal, c, "지금까지의 대화를 두 문장으로 요약해 줘. 비밀 값은 포함하지 마.", "summary", token)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	alphaAssertNoPersistedSecret(t, h.root, token)
	core = start()
	c, _ = conversationClient(t, h)
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	alphaScreen(t, h, terminal, "summary-restored", func(s string) bool { return strings.Contains(s, "두 문장으로 요약") }, token)
	alphaOwnerTurn(t, h, terminal, c, "비밀 값 없이 텃밭 이름과 품종만 한 번 더 말해 줘.", "final-recall", token)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	alphaAssertNoPersistedSecret(t, h.root, token)
}

func alphaOwnerTurn(t *testing.T, h *harness, terminal *terminal, c *clientapi.Client, input, stage, forbidden string) string {
	t.Helper()
	h.keys(terminal, "\x1b[200~"+input+"\x1b[201~\r")
	return alphaOwnerReply(t, h, terminal, c, stage, forbidden)
}

func alphaOwnerReply(t *testing.T, h *harness, terminal *terminal, c *clientapi.Client, stage, forbidden string) string {
	t.Helper()
	var text strings.Builder
	var lastText string
	active := false
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal("owner conversation ended before its result:", err)
		}
		if frame.Error != nil {
			t.Fatal("owner conversation refused:", frame.Error.Code)
		}
		switch item := frame.Payload.(type) {
		case protocol.ApprovalRequest:
			alphaLiveApproval(t, h, terminal, item)
		case protocol.ItemDone:
			if item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil {
				text.WriteString(*item.Text)
				lastText = *item.Text
			}
		case protocol.Result:
			if item.Text != nil {
				text.WriteString(*item.Text)
				lastText = *item.Text
			}
		case protocol.TurnState:
			if item.Active != nil && *item.Active {
				active = true
			}
			if item.Active != nil && !*item.Active && active {
				result := text.String()
				if result == "" {
					t.Fatal("real model turn has no final answer")
				}
				if forbidden != "" && strings.Contains(result, forbidden) {
					t.Fatal("real model repeated the synthetic credential")
				}
				// Match a short final-answer prefix in rendered cells, not a fixed reply.
				visible := strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "#", "").Replace(lastText)
				first, _, _ := strings.Cut(strings.TrimSpace(visible), "\n")
				prefix := []rune(first)
				if len(prefix) > 18 {
					prefix = prefix[:18]
				}
				alphaScreen(t, h, terminal, stage, func(s string) bool { return strings.Contains(s, string(prefix)) }, forbidden)
				return result
			}
		}
	}
}

func alphaAssertNoPersistedSecret(t *testing.T, root, token string) {
	t.Helper()
	for _, area := range []string{"canon", "materials", "derived", "versions"} {
		err := filepath.WalkDir(filepath.Join(root, area), func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(token)) {
				return fmt.Errorf("synthetic credential persisted in %s", area)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAlphaOwnerCodingAndQuestion(t *testing.T) {
	h, start := alphaOwnerHarness(t, true)
	workdir := t.TempDir()
	core := start()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	waitingTopic := "waiting"
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &waitingTopic}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Receive(h.ctx); err != nil {
		t.Fatal(err)
	}
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, "")
	input := fmt.Sprintf("Codex 작업자에게 작은 Python 코딩 작업을 맡겨 줘. 작업 폴더는 %s야. greeting.py에 greet(name)이 '안녕, ' + name을 반환하도록 구현해. 작업자가 먼저 나에게 사용할 이름을 질문하고, 내 답을 받은 뒤 그 이름으로 테스트를 실행하게 해. 성공하면 result.json에 name, greeting, tests_passed 필드를 저장해. 파일은 이 폴더 안에만 만들고, 각 Python 파일 첫 줄에 Apache-2.0 SPDX 주석을 넣어. 원격 작업은 필요 없어. 끝나면 네가 결과 파일을 읽고 한국어로 알려 줘.", workdir)
	h.keys(terminal, "\x1b[200~"+input+"\x1b[201~\r")
	var question protocol.Question
	var spokenQuestion string
	for question.Card == nil && spokenQuestion == "" {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal("worker question did not arrive:", err)
		}
		if frame.Error != nil {
			t.Fatal("coding handoff refused:", frame.Error.Code)
		}
		switch p := frame.Payload.(type) {
		case protocol.TurnState:
			if p.Active != nil && !*p.Active {
				spokenQuestion = alphaSpokenQuestion(t, h, conversation)
			}
		case protocol.ItemDone:
			if p.Speaker != nil && *p.Speaker == protocol.ConversationSpeakerAgent && p.Text != nil {
				spokenQuestion = alphaSpokenQuestion(t, h, conversation)
			}
		case protocol.ApprovalRequest:
			alphaLiveApproval(t, h, terminal, p)
		case protocol.Question:
			if p.Form == nil || p.Form.PluginName != "Codex" {
				t.Fatal("question did not originate from the Codex worker")
			}
			question = p
		}
	}
	if question.Card != nil {
		alphaScreen(t, h, terminal, "worker-question", func(s string) bool { return strings.Contains(s, question.Card.Summary) }, "")
		if len(question.Form.Fields) != 1 || question.Form.Fields[0].Type != protocol.FormFieldTypeText {
			t.Fatal("worker did not ask one text question")
		}
		h.keys(terminal, "\x1b[?3u\x1b[17~")
		field := question.Form.Fields[0].Name + " (text):"
		alphaScreen(t, h, terminal, "question-form", func(s string) bool { return strings.Contains(s, field) }, "")
		h.keys(terminal, "\x1b[200~민준\x1b[201~")
		alphaScreen(t, h, terminal, "answer-draft", func(s string) bool { return strings.Contains(s, "민준") }, "")
		h.keys(terminal, "\r")
		alphaScreen(t, h, terminal, "answer-delivered", func(s string) bool { return strings.Contains(s, design.Text(design.Korean, "resolved")) }, "")
		h.keys(terminal, "\x1b[27u")
	} else {
		// A worker can finish its turn with a question. Verify its durable source,
		// then answer through the same conversation so LINA must route the reply.
		alphaScreen(t, h, terminal, "question-in-conversation", func(screen string) bool {
			return alphaQuestionVisible(spokenQuestion, screen)
		}, "")
		h.keys(terminal, "\x1b[200~이름은 민준이야. 그 답을 같은 Codex 작업자에게 전하고 작업을 계속해 줘.\x1b[201~\r")
	}
	// A spoken question leaves the worker idle until its continuation starts;
	// a structured answer may let it finish before the overlay closes. Neither
	// requires a transient card here. The held-worker journey checks the card;
	// this journey requires the delivered result and verifies its artifact below.
	alphaConversationScreen(t, h, terminal, design.Korean)

	// A result is accepted only after a persona turn follows an actual worker
	// completion. An initial delegation acknowledgement cannot satisfy this loop.
	delivered := false
	for !delivered {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal("worker result did not return:", err)
		}
		switch p := frame.Payload.(type) {
		case protocol.ApprovalRequest:
			alphaLiveApproval(t, h, terminal, p)
		case protocol.ItemDone:
			if p.Speaker != nil && *p.Speaker == protocol.ConversationSpeakerAgent && p.Text != nil && strings.Contains(*p.Text, "민준") {
				if _, err := os.Stat(filepath.Join(workdir, "result.json")); err == nil {
					delivered = true
				}
			}
		case protocol.Result:
			if p.Text != nil && strings.Contains(*p.Text, "민준") {
				if _, err := os.Stat(filepath.Join(workdir, "result.json")); err == nil {
					delivered = true
				}
			}
		}
	}
	alphaScreen(t, h, terminal, "coding-result", func(s string) bool { return strings.Contains(s, "민준") }, "")
	raw, err := os.ReadFile(filepath.Join(workdir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Name, Greeting string
		Passed         bool `json:"tests_passed"`
	}
	if json.Unmarshal(raw, &artifact) != nil || artifact.Name != "민준" || artifact.Greeting != "안녕, 민준" || !artifact.Passed {
		t.Fatal("worker artifact does not prove use of the person's answer")
	}
	if _, versions := alphaLastWorkerOutput(t, h); versions != 1 {
		t.Fatal("answer did not continue the original worker session")
	}
	// Execute the delivered function independently with no inherited credentials.
	verify := exec.CommandContext(h.ctx, "python3", "-c", `from greeting import greet; assert greet("민준") == "안녕, 민준"; assert greet("수아") == "안녕, 수아"`)
	verify.Dir = workdir
	verify.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + workdir, "PYTHONDONTWRITEBYTECODE=1"}
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("delivered function failed independent verification: %v\n%s", err, output)
	}

	sources, err := os.ReadDir(workdir)
	if err != nil {
		t.Fatal(err)
	}
	evidence := map[string]any{"status": "passed", "worker_sessions": 1, "independent_inputs": []string{"민준", "수아"}, "sources": map[string]string{}}
	hashes := evidence["sources"].(map[string]string)
	for _, entry := range sources {
		name := entry.Name()
		if !strings.HasSuffix(name, ".py") && name != "result.json" {
			continue
		}
		if !entry.Type().IsRegular() {
			t.Fatal("worker result is not a regular file")
		}
		data, err := os.ReadFile(filepath.Join(workdir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(name, ".py") && !bytes.HasPrefix(data, []byte("# SPDX-License-Identifier: Apache-2.0\n")) {
			t.Fatal("generated Python source is missing its license header")
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(data))
		if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
			if err := os.WriteFile(filepath.Join(directory, t.Name(), name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if hashes["greeting.py"] == "" || hashes["result.json"] == "" {
		t.Fatal("worker did not deliver the required artifacts")
	}
	if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
		if err := os.WriteFile(filepath.Join(directory, t.Name(), "artifact-verification.json"), mustJSON(t, evidence), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("delivered artifact independently passed both inputs; one worker session")

	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	core = start()
	c, _ = conversationClient(t, h)
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	alphaScreen(t, h, terminal, "result-restored", func(s string) bool { return strings.Contains(s, "민준") }, "")
	reply := alphaOwnerTurn(t, h, terminal, c, "방금 맡겼던 인사 함수의 결과와 내가 답한 이름을 알려 줘. 새 작업을 시작하지 마.", "result-recalled", "")
	if !strings.Contains(reply, "민준") {
		t.Error("LINA lost the worker result after restart")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
}

// A Korean request can end with a period ("이름을 알려 주세요."). The
// durable worker completion establishes its origin; punctuation does not.
func alphaNameRequest(text string) bool {
	text = strings.ToLower(text)
	if !strings.Contains(text, "이름") && !strings.Contains(text, "name") {
		return false
	}
	for _, cue := range []string{"?", "알려", "입력", "어떤", "무엇", "which", "what", "tell", "provide"} {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func alphaQuestionVisible(reply, screen string) bool {
	return alphaNameRequest(screen)
}

func alphaSpokenQuestion(t *testing.T, h *harness, conversation string) string {
	t.Helper()
	workerText, _ := alphaLastWorkerOutput(t, h)
	if alphaNameRequest(workerText) {
		return workerText
	}
	return ""
}

func alphaLastWorkerOutput(t *testing.T, h *harness) (string, int) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var output string
	var versions int
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE id LIKE 'worker-version:%'").Scan(&versions); err != nil {
			return err
		}
		rows, err := tx.QueryContext(h.ctx, "SELECT body FROM conv_event WHERE scaffold_kind='worker_completion' ORDER BY rowid DESC LIMIT 1")
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var result struct{ Output string }
			if err := json.Unmarshal([]byte(body), &result); err != nil {
				return err
			}
			output = result.Output
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return output, versions
}

func TestAlphaOwnerTodoAndMissingCodex(t *testing.T) {
	h, start := alphaOwnerHarness(t, false) // Real model; intentionally empty Core PATH.
	core := start()
	c, _ := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, "")
	const title, deadline = "알파 시험 영수증 제출", "2027-03-15"
	alphaOwnerTurn(t, h, terminal, c, "2027-03-15 마감으로 '알파 시험 영수증 제출' 할 일을 하나 저장해 줘.", "todo-created", "")
	var todoID string
	check := func() {
		db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.Background())
		var got string
		var count int
		err = db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, "SELECT coalesce(min(id),''),count(*) FROM records_todo WHERE deadline=?", deadline).Scan(&got, &count)
		})
		if err != nil || count != 1 || got == "" {
			t.Fatal("real model did not save exactly one deadline todo")
		}
		if todoID != "" && todoID != got {
			t.Error("restart replaced the todo identity")
		}
		todoID = got
	}
	check()
	listed := alphaOwnerTurn(t, h, terminal, c, "할 일 목록에서 알파 시험 영수증 제출의 이름과 마감일을 보여 줘.", "todo-listed", "")
	if !strings.Contains(listed, title) {
		t.Error("real model did not list the saved todo")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	core = start()
	c, _ = conversationClient(t, h)
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	alphaScreen(t, h, terminal, "history-restored", func(s string) bool { return strings.Contains(s, "영수증") }, "")
	listed = alphaOwnerTurn(t, h, terminal, c, "재시작했어. 알파 시험 영수증 제출 할 일이 남아 있는지 목록에서 확인하고 마감일도 알려 줘.", "todo-restored", "")
	if !strings.Contains(listed, title) {
		t.Error("real model lost the saved todo after restart")
	}
	check()
	workdir := t.TempDir()
	refusal := alphaOwnerTurn(t, h, terminal, c, "Codex 작업자에게 "+workdir+" 폴더에 hello.txt를 만들고 '안녕'을 적는 일을 맡겨 줘. 작업자가 없으면 그대로 알려 줘.", "codex-unavailable", "")
	negative := false
	for _, word := range []string{"없", "못", "불가", "설치", "사용할 수"} {
		negative = negative || strings.Contains(refusal, word)
	}
	if !strings.Contains(strings.ToLower(refusal), "codex") || !negative {
		t.Error("real model did not explain Codex unavailability")
	}
	if _, versions := alphaLastWorkerOutput(t, h); versions != 0 {
		t.Error("an unavailable Codex was nevertheless started")
	}
	alphaOwnerTurn(t, h, terminal, c, "그럼 작업은 시작하지 말고 한국어로 짧게 인사해 줘.", "chat-after-refusal", "")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
}
