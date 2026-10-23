// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkerCompletionAfterContextPacket(t *testing.T) {
	message := func(role, content string) json.RawMessage {
		return mustJSON(t, map[string]string{"role": role, "content": content})
	}
	completion := message("user", `External evidence: <external_evidence origin="worker-session:fixture">PANEL_RESULT</external_evidence>`)
	clock := message("developer", "2026-10-23T12:00:00Z\nReferences: [{\"id\":\"clock:turn:fixture\"}]")
	memory := message("user", `External evidence: <external_evidence origin="context:memory">A saved fact.</external_evidence>`)
	t.Run("current completion before packet", func(t *testing.T) {
		response := httptest.NewRecorder()
		if !respondWorkerCompletion(response, []json.RawMessage{completion, clock, memory}, "PANEL_RESULT") || !strings.Contains(response.Body.String(), "PANEL_RESULT") {
			t.Fatal("ContextPacket evidence hid the current worker completion")
		}
	})
	t.Run("old completion before new person input", func(t *testing.T) {
		response := httptest.NewRecorder()
		if respondWorkerCompletion(response, []json.RawMessage{completion, message("user", "Start another job"), clock, memory}, "PANEL_RESULT") {
			t.Fatal("history completion handled as current input")
		}
	})
}
