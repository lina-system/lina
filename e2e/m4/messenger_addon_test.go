// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/sandbox"
)

// addonRoot prepares a headless state root whose canon already holds the
// person's connected source and one synced chat. Connecting a source is the
// person's act, so Core never creates it.
func addonRoot(t *testing.T, platform, account string, extras ...messenger.Source) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"system/core", "canon"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "system", "core", "main.json"), []byte(`{"main_id":"messenger-addon"}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root)
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		controls.Close()
		t.Fatal(err)
	}
	epoch, err := controls.MintEpoch(0, 0)
	if err != nil {
		controls.Close()
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		controls.Close()
		t.Fatal(err)
	}
	source := messenger.Source{Person: "person:messenger-addon", Platform: platform, Account: account,
		Mode: messenger.ModeUnofficial, Adapter: "messenger-addon", State: messenger.StateConnected,
		AcceptedTerms: &messenger.Terms{Person: "person:messenger-addon", At: time.Now(), Text: "synthetic acceptance"},
		ConnectedAt:   time.Now()}
	store := messenger.Open(db, nil, nil)
	if err := store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	for _, extra := range extras {
		extra.Person = source.Person
		if err := store.Connect(t.Context(), extra); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertChats(t.Context(), extra, []messenger.Chat{{ID: "room-1", Name: "Room", Kind: "group"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	controls.Close()
	return root
}

func requireAddonSandbox(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	box := sandbox.New(owner, sandbox.Config{WorkDir: dir})
	l := lease.New(func() {})
	defer l.Release()
	if status := box.Probe(t.Context(), l); !status.Available {
		t.Skipf("OS sandbox unavailable: %s", status.Cause)
	}
}

func startAddonCore(t *testing.T, root string, cfg addon.Config, extra map[string]messenger.Adapter) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- boot.Run(ctx, boot.Config{StateRoot: root, Socket: filepath.Join(root, "core.sock"), Messenger: &boot.MessengerConfig{
			Addon: addon.NewManager(cfg), Adapters: extra, Interval: 25 * time.Millisecond,
		}})
	}()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Core stopped with %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Core shutdown timed out")
		}
	}
}

func readCanon(t *testing.T, root string, query string, args ...any) int {
	t.Helper()
	db, err := canon.OpenReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func readString(t *testing.T, root, query string, args ...any) string {
	t.Helper()
	db, err := canon.OpenReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var value string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), query, args...).Scan(&value)
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func sourceRows(t *testing.T, root string) string {
	t.Helper()
	db, err := canon.OpenReader(t.Context(), filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return err.Error()
	}
	defer db.Close(context.Background())
	var rows string
	_ = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT coalesce(group_concat(platform||':'||state||':'||failure, ' | '),'') FROM messenger_source`).Scan(&rows)
	})
	return rows
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestM4MessengerAddonKilledMidPage proves the resumption rule: an add-on
// killed in the middle of a chat leaves the cursor where the last committed page
// put it, and the next run completes the page exactly once.
func TestM4MessengerAddonKilledMidPage(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	program, err := filepath.Abs("../../adapters/messenger/fixture-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	root := addonRoot(t, "fixture", "account-1")
	dir := addon.DataDir(root, "fixture", "account-1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := `{
	  "chats": [{"id":"dm-1","name":"Direct","kind":"direct"}],
	  "pages": {
	    "": {"messages":[{"id":"m-1","author":{"id":"acc-1","name":"Me"},"route":"person","at":"2026-10-24T09:00:00Z","text":"one"}],"deletions":[],"next":"c1","hasMore":true},
	    "c1": {"messages":[{"id":"m-2","author":{"id":"u1","name":"Someone"},"route":"external_participant","at":"2026-10-24T09:01:00Z","text":"two"}],"deletions":[],"next":"end","hasMore":false,"through":"2026-10-24T09:01:00Z"}
	  },
	  "blockCursor": "c1"
	}`
	if err := os.WriteFile(filepath.Join(dir, "script.json"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	stop := startAddonCore(t, root, addon.Config{Node: node, Program: program, StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5, RequestTimeout: 30 * time.Second}, nil)
	defer func() { stop() }()

	deadline := time.Now().Add(30 * time.Second)
	for readCanon(t, root, `SELECT count(*) FROM messenger_record`) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("no committed page; sources: %s; records: %s", sourceRows(t, root), filepath.Join(root, "plugins", "messenger-addon"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, "the add-on blocked mid-page", func() bool {
		_, err := os.Stat(filepath.Join(dir, "blocked"))
		return err == nil
	})
	if cursor := readString(t, root, `SELECT cursor FROM messenger_chat`); cursor != "c1" {
		t.Fatalf("before kill cursor = %q", cursor)
	}
	if err := os.WriteFile(filepath.Join(dir, "kill"), []byte("kill"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed page", func() bool { return readString(t, root, `SELECT state FROM messenger_source`) == "failing" })
	if cursor := readString(t, root, `SELECT cursor FROM messenger_chat`); cursor != "c1" {
		t.Fatalf("after kill cursor = %q", cursor)
	}
	stop()
	stop = startAddonCore(t, root, addon.Config{Node: node, Program: program, StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5, RequestTimeout: 30 * time.Second}, nil)
	waitFor(t, "the completion after the kill", func() bool {
		return readString(t, root, `SELECT cursor FROM messenger_chat`) == "end"
	})
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_record`); got != 2 {
		t.Fatalf("records = %d, want 2", got)
	}
	for _, id := range []string{"m-1", "m-2"} {
		if got := readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE native_id=?`, id); got != 1 {
			t.Fatalf("record %s stored %d times", id, got)
		}
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_coverage`); got != 1 {
		t.Fatalf("coverage rows = %d, want 1", got)
	}
	if state := readString(t, root, `SELECT state FROM messenger_source`); state != "connected" {
		t.Fatalf("source state = %q", state)
	}
}

// TestM4MessengerAddonFailsWithoutNode proves the failure stays with its source:
// without Node.js the add-on source shows that cause, and another source still
// synced.
func TestM4MessengerAddonFailsWithoutNode(t *testing.T) {
	t.Parallel()
	root := addonRoot(t, "fixture", "account-1", messenger.Source{Platform: "other", Account: "other-account", Adapter: "other", Mode: messenger.ModeOfficial, State: messenger.StateConnected})
	stop := startAddonCore(t, root, addon.Config{StateRoot: root, Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond}, map[string]messenger.Adapter{"other": &otherSource{}})
	defer func() { stop() }()

	waitFor(t, "the add-on source failing with its cause", func() bool {
		state := readString(t, root, `SELECT state FROM messenger_source WHERE platform='fixture'`)
		return state == "failing"
	})
	cause := readString(t, root, `SELECT failure FROM messenger_source WHERE platform='fixture'`)
	if !strings.Contains(cause, "Node.js") {
		t.Fatalf("failure cause = %q, want it to name Node.js", cause)
	}
	waitFor(t, "the other source to keep syncing", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE platform='other'`) == 1
	})
	if state := readString(t, root, `SELECT state FROM messenger_source WHERE platform='other'`); state != "connected" {
		t.Fatalf("other source state = %q", state)
	}
}

// otherSource is a ported adapter for one official platform; it stands in for
// every adapter that does not depend on the add-on.
type otherSource struct{}

func (*otherSource) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	return messenger.SignedIn, nil
}
func (*otherSource) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "room-1", Name: "Room", Kind: "group"}}, nil
}
func (*otherSource) Page(_ context.Context, _ messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if cursor != "" {
		return messenger.Page{Next: cursor}, nil
	}
	return messenger.Page{Messages: []messenger.Message{{ID: chat + "-m1", Author: messenger.Author{ID: "other-user"}, Route: messenger.RouteParticipant, At: time.Now(), Text: "kept"}}, Next: "done"}, nil
}
func (*otherSource) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (*otherSource) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (*otherSource) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (*otherSource) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Interval: time.Second}
}
