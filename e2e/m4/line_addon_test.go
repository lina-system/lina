// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger/addon"
)

// lineScript is the recorded synthetic LINE platform the add-on's real driver
// answers from: one direct chat whose newest messages overlap between reads,
// one of them unreadable, and a signed-in account.
const lineScript = `{
  "credentials": {"account_id":"acc-1","auth_token":"synthetic","device":"ANDROIDSECONDARY"},
  "profile": {"mid":"acc-1","display_name":"Me"},
  "chats": [{"chat_id":"dm-1","type":"user","display_name":"Direct"}],
  "messages": [
    {"message_id":"m-3","chat_id":"dm-1","author_id":"u1","author_name":"Someone","text":null,"decryption_error":{"code":"missing_e2ee_key","message":"no key"},"content_type":"NONE","sent_at":"2026-10-24T09:03:00.000Z"},
    {"message_id":"m-2","chat_id":"dm-1","author_id":"u1","author_name":"Someone","text":"second","content_type":"NONE","sent_at":"2026-10-24T09:02:00.000Z"},
    {"message_id":"m-1","chat_id":"dm-1","author_id":"acc-1","author_name":"Me","text":"first","content_type":"NONE","sent_at":"2026-10-24T09:01:00.000Z"}
  ]
}`

func writeLineScript(t *testing.T, root, script string) {
	t.Helper()
	dir := addon.DataDir(root, "line", "acc-1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.json"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
}

func lineProgram(t *testing.T) string {
	t.Helper()
	program, err := filepath.Abs("../../adapters/messenger/line-fixture-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// TestM4LineNewestReadsStoreEachMessageOnce proves LINE's coverage rule through
// the running Core: LINE serves no cursor across a restart, so the second run
// reads the same newest messages again, stores each once, keeps the unreadable
// message as a flagged record with no text, and records that the covered range
// is newest-N only.
func TestM4LineNewestReadsStoreEachMessageOnce(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	root := addonRoot(t, "line", "acc-1")
	writeLineScript(t, root, lineScript)
	program := lineProgram(t)
	config := addon.Config{Node: node, Program: program, StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5, RequestTimeout: 30 * time.Second}

	stop := startAddonCore(t, root, config, nil)
	waitFor(t, "the first newest-N read", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record`) == 3
	})
	firstRead := readString(t, root, `SELECT read_at FROM messenger_coverage WHERE chat='dm-1'`)
	// The session and the Letter Sealing keys live only in the account's data
	// directory, which Core keeps 0700.
	if info, err := os.Stat(addon.DataDir(root, "line", "acc-1")); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("add-on data directory mode: %v %v", info.Mode().Perm(), err)
	}
	stop()

	stop = startAddonCore(t, root, config, nil)
	defer stop()
	waitFor(t, "the second read after the restart", func() bool {
		return readString(t, root, `SELECT read_at FROM messenger_coverage WHERE chat='dm-1'`) != firstRead
	})

	for _, id := range []string{"m-1", "m-2", "m-3"} {
		if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE message=?`, id); got != 1 {
			t.Fatalf("message %s stored %d times", id, got)
		}
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE decryption_failed=1`); got != 1 {
		t.Fatalf("flagged records = %d, want 1", got)
	}
	if got := readCanon(t, root, `SELECT count(*) FROM conv_event e JOIN messenger_record r ON r.event=e.id WHERE r.message='m-3'`); got != 1 {
		t.Fatalf("flagged message has %d events", got)
	}
	if body := readString(t, root, `SELECT e.body FROM conv_event e JOIN messenger_record r ON r.event=e.id WHERE r.message='m-3'`); body != "" {
		t.Fatalf("unreadable message carries text %q", body)
	}
	if body := readString(t, root, `SELECT e.body FROM conv_event e JOIN messenger_record r ON r.event=e.id WHERE r.message='m-2'`); body != "second" {
		t.Fatalf("readable message body = %q", body)
	}
	if depth := readString(t, root, `SELECT history_depth FROM messenger_coverage WHERE chat='dm-1'`); depth != "newest-N" {
		t.Fatalf("history depth = %q", depth)
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_coverage WHERE chat='dm-1' AND cursor_survives_restart=0`); got != 1 {
		t.Fatalf("coverage does not say no cursor survives a restart")
	}
	if state := readString(t, root, `SELECT state FROM messenger_source`); state != "connected" {
		t.Fatalf("source state = %q", state)
	}
}

// TestM4LineRevokedSessionNeedsSignIn proves a revoked LINE session asks for
// sign-in instead of storing records or failing the source with a generic error.
func TestM4LineRevokedSessionNeedsSignIn(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	root := addonRoot(t, "line", "acc-1")
	writeLineScript(t, root, `{
  "credentials": {"account_id":"acc-1","auth_token":"revoked","device":"ANDROIDSECONDARY"},
  "revoked": true,
  "chats": [{"chat_id":"dm-1","type":"user","display_name":"Direct"}],
  "messages": [{"message_id":"m-1","chat_id":"dm-1","author_id":"u1","text":"secret","content_type":"NONE","sent_at":"2026-10-24T09:01:00.000Z"}]
}`)
	stop := startAddonCore(t, root, addon.Config{Node: node, Program: lineProgram(t), StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5, RequestTimeout: 30 * time.Second}, nil)
	defer stop()

	waitFor(t, "the revoked source needing sign-in", func() bool {
		return readString(t, root, `SELECT state FROM messenger_source`) == "needs_sign_in"
	})
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_record`); got != 0 {
		t.Fatalf("revoked session stored %d records", got)
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_chat WHERE cursor <> ''`); got != 0 {
		t.Fatalf("revoked session moved the cursor on %d chats", got)
	}
}
