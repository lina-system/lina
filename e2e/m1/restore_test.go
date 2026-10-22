// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type restoreScenario struct {
	Targets          []string   `json:"targets"`
	InvalidArguments [][]string `json:"invalid_arguments"`
	Deliveries       []struct {
		Before string `json:"before"`
		After  string `json:"after"`
	} `json:"deliveries"`
}

func readRestoreScenario(t *testing.T) restoreScenario {
	t.Helper()
	data, err := os.ReadFile("restore.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario restoreScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

func TestRestoreCommandBoundaries(t *testing.T) {
	t.Parallel()

	scenario := readRestoreScenario(t)
	binary := buildLina(t)
	h := newHarness(t, binary)
	help := h.command("restore", "--help")
	help.start(t)
	help.wait(t, 0)
	for _, args := range scenario.InvalidArguments {
		p := h.command(append([]string{"restore"}, args...)...)
		p.start(t)
		p.wait(t, 64)
		if !strings.Contains(p.stderr.String(), "lina restore") {
			t.Fatal("missing restore argument help")
		}
	}
	if err := os.MkdirAll(filepath.Join(h.root, "staging/restore.pending"), 0700); err != nil {
		t.Fatal(err)
	}
	core := h.startCore()
	core.wait(t, 78)
	if !strings.Contains(core.stderr.String(), "restore incomplete") {
		t.Fatalf("wrong refusal: %s", &core.stderr)
	}
	for _, path := range []string{h.socket, filepath.Join(h.root, "system/core/epoch")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("incomplete restore started Core")
		}
	}
}

func TestM1RestoreInstalledConversation(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Linux installation")
	}
	scenario := readRestoreScenario(t)
	for _, targetKind := range scenario.Targets {
		t.Run(targetKind, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { emitResponse(w, "synthetic restore reply", true) }))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			c, conversation := conversationClient(t, h)
			admittedTurn(t, h, c, conversation, "before-backup", "synthetic before backup")
			c.Close()
			h.stopCore(core)
			// Capture pending channel sends alongside a real committed reply.
			db, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			for _, delivery := range scenario.Deliveries {
				_, err := db.ExecContext(h.ctx, `INSERT INTO channel_outbound(id,address,bot,chat,topic,agent,ledger_ref,reply_to,parts,state)
 SELECT ?, '["telegram","fixture","123",""]', '["telegram","fixture"]', '123', '', agent_id, id, '', '["synthetic reply"]', ?
 FROM conv_event WHERE speaker_kind='agent' LIMIT 1`, delivery.Before, delivery.Before)
				if err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			p := h.command("backup", "--state-root", h.root)
			p.start(t)
			p.wait(t, 0)
			entries, err := os.ReadDir(filepath.Join(h.root, "backups"))
			if err != nil {
				t.Fatal(err)
			}
			generation := ""
			for _, entry := range entries {
				if entry.IsDir() {
					generation = filepath.Join(h.root, "backups", entry.Name())
				}
			}
			if generation == "" {
				t.Fatal("no generation")
			}
			core = start(true)
			c, conversation = conversationClient(t, h)
			admittedTurn(t, h, c, conversation, "after-backup", "synthetic after backup")
			running := h.command("restore", "--state-root", h.root, "--generation", generation)
			running.start(t)
			running.wait(t, 69)
			c.Close()
			h.stopCore(core)
			source := h.root
			if targetKind == "other" {
				other := newHarness(t, h.binary)
				for _, path := range []string{"system/core/main.json", "system/core/epoch", "system/bin/manifest.json"} {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(other.root, path)), 0700); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(filepath.Join(source, path))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(other.root, path), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				h = other
			}
			before, err := os.ReadFile(filepath.Join(h.root, "system/core/epoch"))
			if err != nil {
				t.Fatal(err)
			}
			marker, _ := json.Marshal(map[string]any{"schema": 1, "epoch": string(before), "cause": "sqlite_corrupt"})
			if err := os.WriteFile(filepath.Join(h.root, "system/core/halt.json"), marker, 0600); err != nil {
				t.Fatal(err)
			}
			restore := h.command("restore", "--state-root", h.root, "--generation", generation)
			restore.start(t)
			restore.wait(t, 0)
			// Check before Core recovery can alter the restored delivery states.
			db, err = canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			for _, delivery := range scenario.Deliveries {
				var state string
				if err := db.QueryRowContext(h.ctx, "SELECT state FROM channel_outbound WHERE id=?", delivery.Before).Scan(&state); err != nil {
					db.Close()
					t.Fatal(err)
				}
				if state != delivery.After {
					t.Errorf("restored %s delivery is %s, want %s", delivery.Before, state, delivery.After)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(h.root, "system/core/halt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("verified restore retained halt latch")
			}
			after, err := os.ReadFile(filepath.Join(h.root, "system/core/epoch"))
			if err != nil || string(before) != string(after) {
				t.Fatal("restore replaced control epoch")
			}
			core = h.command("core", "--state-root", h.root, "--socket", h.socket)
			core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
			core.start(t)
			h.connect(core).Close()
			c, restoredConversation := conversationClient(t, h)
			if restoredConversation != conversation {
				t.Fatal("restored another conversation")
			}

			probe, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "restore-test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			topic := "conversation:" + conversation
			if _, err := probe.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			envelope, err := probe.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, ok := envelope.Payload.(protocol.Snapshot)
			if !ok {
				t.Fatalf("no restored snapshot: %+v", envelope)
			}
			encoded, _ := json.Marshal(snapshot)
			if !strings.Contains(string(encoded), "synthetic before backup") || strings.Contains(string(encoded), "synthetic after backup") {
				t.Fatalf("mixed conversation history: %s", encoded)
			}
			probe.Close()

			current, err := os.ReadFile(filepath.Join(h.root, "system/core/epoch"))
			if err != nil {
				t.Fatal(err)
			}
			oldEpoch, _ := strconv.ParseUint(string(before), 10, 64)
			newEpoch, _ := strconv.ParseUint(string(current), 10, 64)
			if newEpoch <= oldEpoch {
				t.Fatal("restored Core did not advance epoch")
			}
			admittedTurn(t, h, c, conversation, "after-restore", "synthetic after restore")
			c.Close()
			h.stopCore(core)
		})
	}
}
