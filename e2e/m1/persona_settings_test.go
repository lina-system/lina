// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Seed a synthetic preset while the installed Core is stopped. Owner-authored
// preset files are never changed; the next Core process uses the normal store.
func seedPersonaAgent(t *testing.T, h *harness, person string) string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpdatePreset(h.ctx, agents.Preset{ID: "fixture", Core: "fixture core", DefaultVoice: "fixture default voice", BlankSettings: []string{"likes", "birthday"}}, person); err != nil {
		t.Fatal(err)
	}
	a, err := store.Create(h.ctx, agents.Create{Name: "Fixture", PersonID: person, Preset: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(h.ctx, "UPDATE agent_first_run SET agent_id=? WHERE singleton=1", a.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestPersonaBlankFilledFromConversationAndPreparedAfterRestart(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	var prefix string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input, Tools []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		if len(request.Input) < 1 {
			t.Error("missing stable prefix")
			emitResponse(w, "Failed.", true)
			return
		}
		fixed := request.Instructions + string(mustJSON(t, request.Input[:1]))
		if n == 1 {
			prefix = fixed
		} else if fixed != prefix {
			t.Error("blank values changed cached prefix")
		}
		var settings string
		for _, raw := range request.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(raw, &item) == nil && strings.Contains(item.Content, "persona_settings") {
				settings = item.Content
				if item.Role != "user" {
					t.Error("filled values became instructions")
				}
			}
		}
		if settings == "" {
			t.Error("preset blanks absent from ContextPacket")
		}
		if n == 3 && !strings.Contains(settings, "violet") {
			t.Error("filled setting absent after restart")
		}
		if n < 3 && strings.Contains(settings, "violet") {
			t.Error("setting packet changed inside its prepared turn")
		}
		if n == 1 {
			emitPersonaCall(w, "fill", "fill_persona_setting", map[string]any{"quote": "Your favorite color is violet.", "name": "likes", "value": "violet"})
		} else if n == 2 {
			var item struct{ Output string }
			json.Unmarshal(request.Input[len(request.Input)-1], &item)
			if !strings.Contains(item.Output, `"code":"ok"`) {
				t.Errorf("fill failed: %s", item.Output)
			}
			emitResponse(w, "Acknowledged.", true)
		} else {
			emitResponse(w, "Violet.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, _ := conversationClient(t, h)
	person := c.Welcome.PersonID
	c.Close()
	h.stopCore(core)
	agent := seedPersonaAgent(t, h, person)
	core = start(true)
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "blank-fill", "Your favorite color is violet.")
	for _, frame := range frames {
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem {
			t.Error("automatic blank fill produced an unsolicited notice")
		}
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "blank-read", "What is your favorite color?")
	reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var value, source string
	if err := reader.QueryRowContext(h.ctx, "SELECT json_extract(state,'$.settings.likes'),json_extract(state,'$.setting_sources.likes') FROM agents WHERE id=?", agent).Scan(&value, &source); err != nil {
		t.Fatal(err)
	}
	var speaker, owner, sourceAgent string
	if err := reader.QueryRowContext(h.ctx, "SELECT speaker_kind,person_id,agent_id FROM conv_event WHERE id=?", source).Scan(&speaker, &owner, &sourceAgent); err != nil {
		t.Fatal(err)
	}
	if value != "violet" || speaker != "person" || owner != person || sourceAgent != agent {
		t.Fatal("blank value has no confirmed owner event")
	}
	c.Close()
	h.stopCore(core)
	if calls.Load() != 3 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}
