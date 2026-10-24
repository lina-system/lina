// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
)

const (
	whatsappMe   = "15550000000@s.whatsapp.net"
	whatsappPeer = "15551234567@s.whatsapp.net"
)

func nodePath(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	return node
}

// whatsappScript is the recorded synthetic platform. history is what the phone
// pushed at link time; live is what arrives while the account stays linked. A
// restarted add-on replays both, so Core's idempotence is what keeps records
// single.
func whatsappScript(history, live string, forbidden bool) string {
	refusal := "false"
	if forbidden {
		refusal = "true"
	}
	return `{
	  "me": {"id": "` + whatsappMe + `", "name": "Me"},
	  "chats": [{"id": "` + whatsappPeer + `", "name": "Ada", "type": "individual"}],
	  "history": [` + history + `],
	  "live": [` + live + `],
	  "forbidden": ` + refusal + `
	}`
}

const whatsappHistory = `{"id":"h-1","chat_id":"` + whatsappPeer + `","from":"` + whatsappPeer + `","from_name":"Ada","timestamp":"2026-10-24T09:00:00.000Z","is_outgoing":false,"type":"text","text":"history one"},
{"id":"h-2","chat_id":"` + whatsappPeer + `","from":"","from_name":"Me","timestamp":"2026-10-24T09:01:00.000Z","is_outgoing":true,"type":"text","text":"history two"}`

const whatsappLive = `{"afterMs":1500,"message":{"id":"l-1","chat_id":"` + whatsappPeer + `","from":"` + whatsappPeer + `","from_name":"Ada","timestamp":"2026-10-24T09:02:00.000Z","is_outgoing":false,"type":"text","text":"live one"}},
{"afterMs":5000,"message":{"id":"l-2","chat_id":"` + whatsappPeer + `","from":"` + whatsappPeer + `","from_name":"Ada","timestamp":"2026-10-24T09:03:00.000Z","is_outgoing":false,"type":"text","text":"live two"}}`

func writeWhatsAppScript(t *testing.T, root, account, script string) {
	t.Helper()
	dir := addon.DataDir(root, "whatsapp", account)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "whatsapp.json"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
}

func whatsappProgram(t *testing.T) string {
	t.Helper()
	program, err := filepath.Abs("../../adapters/messenger/fixture-whatsapp-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// TestM4WhatsAppHistoryAndLiveSurviveARestart proves the two reads the platform
// offers: the history pushed at link time and live messages. A restart mid-stream
// resumes without storing a message twice.
func TestM4WhatsAppHistoryAndLiveSurviveARestart(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node := nodePath(t)
	program := whatsappProgram(t)
	root := addonRoot(t, "whatsapp", "account-1")
	writeWhatsAppScript(t, root, "account-1", whatsappScript(whatsappHistory, whatsappLive, false))
	config := addon.Config{Node: node, Program: program, StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5, RequestTimeout: 30 * time.Second}
	stop := startAddonCore(t, root, config, nil)
	defer func() { stop() }()

	waitFor(t, "the history and the first live message", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record`) == 3
	})
	for _, id := range []string{"h-1", "h-2", "l-1"} {
		if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE message=?`, id); got != 1 {
			t.Fatalf("record %s stored %d times before restart", id, got)
		}
	}
	cursor := readString(t, root, `SELECT cursor FROM messenger_chat WHERE chat=?`, whatsappPeer)
	if cursor == "" {
		t.Fatal("the committed cursor is empty")
	}

	// Restart in the middle of the stream. The synthetic platform replays what
	// it pushed; the second live message is still to come.
	stop()
	stop = startAddonCore(t, root, config, nil)
	waitFor(t, "the second live message", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record`) == 4
	})
	for _, id := range []string{"h-1", "h-2", "l-1", "l-2"} {
		if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE message=?`, id); got != 1 {
			t.Fatalf("record %s stored %d times after restart", id, got)
		}
	}
	if got := readString(t, root, `SELECT chat FROM messenger_record WHERE message='l-2'`); got != whatsappPeer {
		t.Fatalf("live record chat = %q", got)
	}
	if got := readString(t, root, `SELECT state FROM messenger_source WHERE platform='whatsapp' AND account='account-1'`); got != "connected" {
		t.Fatalf("source state = %q", got)
	}
}

// TestM4WhatsAppForbiddenMarksTheSourceFailing proves a platform refusal stays
// with its source: the failing account names the cause and the other account
// keeps reading.
func TestM4WhatsAppForbiddenMarksTheSourceFailing(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node := nodePath(t)
	program := whatsappProgram(t)
	blocked := messenger.Source{Platform: "whatsapp", Account: "blocked", Mode: messenger.ModeUnofficial, Adapter: "messenger-addon",
		State:         messenger.StateConnected,
		AcceptedTerms: &messenger.Terms{Person: "person:messenger-addon", At: time.Now(), Text: "synthetic acceptance"},
		ConnectedAt:   time.Now()}
	root := addonRoot(t, "whatsapp", "account-1", blocked)
	writeWhatsAppScript(t, root, "account-1", whatsappScript(whatsappHistory, "", false))
	writeWhatsAppScript(t, root, "blocked", whatsappScript("", "", true))
	config := addon.Config{Node: node, Program: program, StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5, RequestTimeout: 30 * time.Second}
	stop := startAddonCore(t, root, config, nil)
	defer func() { stop() }()

	waitFor(t, "the refused account failing", func() bool {
		return readString(t, root, `SELECT state FROM messenger_source WHERE account='blocked'`) == "failing"
	})
	cause := readString(t, root, `SELECT failure FROM messenger_source WHERE account='blocked'`)
	if !strings.Contains(cause, "restricted") {
		t.Fatalf("failure cause = %q", cause)
	}
	waitFor(t, "the other account reading", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE account='account-1'`) == 2
	})
	if got := readString(t, root, `SELECT state FROM messenger_source WHERE account='account-1'`); got != "connected" {
		t.Fatalf("other account state = %q", got)
	}
}
