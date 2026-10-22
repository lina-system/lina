// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1FirstUseKoreanNotices(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Instructions string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		n := calls.Add(1)
		if n == 1 && (!strings.Contains(request.Instructions, "TUI") || !strings.Contains(request.Instructions, "Markdown")) {
			t.Error("TUI request omits its surface formatting instructions")
		}
		switch n {
		case 1:
			emitPersonaCall(w, "change", "change_persona", map[string]any{"quote": "편하게 말해 줘", "user_voice": "편한 한국어 말투"})
		case 3:
			emitPersonaCall(w, "undo", "undo_persona", map[string]any{"quote": "말투 변경을 취소해 줘", "revision": 2})
		case 5, 7:
			emitPersonaCall(w, "emotion", "set_emotion_expression", map[string]any{"quote": "감정 표현을 바꿔 줘", "enabled": n == 7})
		case 9:
			emitPersonaCall(w, "skill", "write_skill", map[string]any{"id": "notice-fixture", "expected": 0, "files": map[string]any{"SKILL.md": map[string]any{"text": "---\nname: notice-fixture\ndescription: A synthetic procedure.\n---\n확인한 사실을 먼저 알려 주세요.\n"}}})
		case 11:
			emitPersonaCall(w, "undo-skill", "undo_skill", map[string]any{"id": "notice-fixture", "revision": 1})
		case 13:
			http.Error(w, "synthetic failure", http.StatusBadRequest)
		default:
			emitResponse(w, "알겠어요.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	terminal := h.startScreenTUI("TERM=xterm-256color", "LC_ALL=ko_KR.UTF-8")
	var notices []string
	for i, input := range []string{"편하게 말해 줘", "말투 변경을 취소해 줘", "감정 표현을 바꿔 줘", "감정 표현을 바꿔 줘", "이 절차를 익혀 줘", "방금 익힌 절차를 취소해 줘", "..."} {
		frames := admittedTurn(t, h, c, conversation, fmt.Sprintf("korean-%d", i), input)
		count := 0
		for _, frame := range frames {
			item, ok := frame.Payload.(protocol.ItemDone)
			if !ok || item.Speaker == nil || *item.Speaker != protocol.ConversationSpeakerSystem || item.Text == nil {
				continue
			}
			count++
			notices = append(notices, *item.Text)
			if !strings.ContainsFunc(*item.Text, func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) {
				t.Errorf("turn %d notice is not Korean: %s", i, *item.Text)
			}
			waitScreen(t, h, terminal, string([]rune(*item.Text)[:min(18, len([]rune(*item.Text)))]))
		}
		if count != 1 {
			t.Errorf("turn %d has %d notices, want one", i, count)
		}
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	topic := "conversation:" + conversation
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if !ok {
		t.Fatalf("expected restored snapshot, got %T", frame.Payload)
	}
	seen := map[string]int{}
	for _, row := range snapshot.Items {
		if row.Kind != protocol.KindItemDone {
			continue
		}
		var item protocol.ItemDone
		if err := json.Unmarshal(row.Payload, &item); err != nil {
			t.Fatal(err)
		}
		if item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem && item.Text != nil {
			seen[*item.Text]++
		}
	}
	for _, notice := range notices {
		if seen[notice] != 1 {
			t.Errorf("restored notice appears %d times", seen[notice])
		}
	}
	c.Close()
	h.stopCore(core)
}
