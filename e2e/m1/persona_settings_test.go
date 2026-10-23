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

func TestPersonaSettingsRetractionThroughInstalledCore(t *testing.T) {
	t.Parallel()
	const colorQuote = "We chose violet as your favorite color."
	const birthdayQuote = "Your birthday is June 12."
	var calls atomic.Int32
	packets := make(chan string, 8)
	memories := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		for _, raw := range request.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(raw, &item) == nil && strings.Contains(item.Content, "persona_settings") {
				if item.Role != "user" {
					t.Error("settings gained instruction authority")
				}
				packets <- item.Content
			}
		}
		if n == 2 || n == 3 || n == 5 {
			var item struct{ Output string }
			var result struct {
				Code   string
				Output struct {
					ID                 string
					Filled, Remembered bool
				}
			}
			if len(request.Input) == 0 || json.Unmarshal(request.Input[len(request.Input)-1], &item) != nil || json.Unmarshal([]byte(item.Output), &result) != nil || result.Code != "ok" {
				t.Errorf("request %d tool failed: %s", n, item.Output)
			} else if n == 3 {
				if !result.Output.Remembered || result.Output.ID == "" {
					t.Error("source memory was not committed")
				}
				memories <- result.Output.ID
			} else if !result.Output.Filled {
				t.Error("setting was not filled")
			}
		}
		switch n {
		case 1:
			emitPersonaCall(w, "color", "fill_persona_setting", map[string]any{"quote": colorQuote, "name": "likes", "value": "violet"})
		case 2:
			emitPersonaCall(w, "source", "record_relationship", map[string]any{"quote": colorQuote})
		case 4:
			emitPersonaCall(w, "birthday", "fill_persona_setting", map[string]any{"quote": birthdayQuote, "name": "birthday", "value": "June 12"})
		default:
			emitResponse(w, "Acknowledged.", true)
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
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "memory", Version: "1.0"})
	admittedTurn(t, h, c, conversation, "retract-color", colorQuote)
	admittedTurn(t, h, c, conversation, "keep-birthday", birthdayQuote)
	reader, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var colorSource, birthdaySource string
	err = reader.QueryRowContext(h.ctx, "SELECT json_extract(state,'$.setting_sources.likes'),json_extract(state,'$.setting_sources.birthday') FROM agents WHERE id=?", agent).Scan(&colorSource, &birthdaySource)
	reader.Close()
	if err != nil || colorSource == "" || birthdaySource == "" || colorSource == birthdaySource {
		t.Fatal("fills lack independent confirmed sources", err)
	}
	for range 5 {
		select {
		case <-packets:
		default:
			t.Fatal("fill request missing persona settings")
		}
	}
	check := func(id string, visible bool) {
		t.Helper()
		admittedTurn(t, h, c, conversation, id, "Which settings are available?")
		select {
		case packet := <-packets:
			if strings.Contains(packet, "violet") != visible || strings.Contains(packet, colorSource) != visible {
				t.Errorf("color exposure after %s: want visible=%v; packet=%s", id, visible, packet)
			}
			if !strings.Contains(packet, "June 12") || !strings.Contains(packet, birthdaySource) {
				t.Errorf("active value or its provenance lost: %s", packet)
			}
		default:
			t.Fatal("prepared settings missing")
		}
	}
	check("before-forget", true)
	var memoryID string
	select {
	case memoryID = <-memories:
	default:
		t.Fatal("missing committed memory receipt")
	}
	op := "forget"
	requestID, err := c.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &agent, ItemID: &memoryID})
	if err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.ReplyTo == nil || *frame.ReplyTo != requestID {
			continue
		}
		if frame.Kind != protocol.KindMemoryResult {
			t.Fatalf("forget failed: %+v", frame)
		}
		break
	}
	check("after-forget", false)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	check("after-restart", false)
	c.Close()
	h.stopCore(core)
	if calls.Load() != 8 {
		t.Fatalf("provider calls=%d, want 8", calls.Load())
	}
}

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
