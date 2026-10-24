// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/internal/core/messenger/discorduser"
)

// discordScript is the recorded SDK state the add-on's Discord fixture answers
// from. The personal account reads guild channel 10 and DM 20; channel 11 is a
// chat the person's bot source already reads. edited makes the guild channel's
// first message report a later edited time.
func discordScript(edited bool) string {
	first := `{"id":"100","channel_id":"10","author":{"id":"7","username":"mina"},"content":"hello","timestamp":"2026-10-24T09:00:00.000Z"}`
	if edited {
		first = `{"id":"100","channel_id":"10","author":{"id":"7","username":"mina"},"content":"hello (edited)","timestamp":"2026-10-24T09:00:00.000Z","edited_timestamp":"2026-10-24T09:06:00.000Z"}`
	}
	return `{
	  "token": "synthetic-token",
	  "me": {"id": "900", "username": "jun"},
	  "servers": [{"id": "1", "name": "Guild A"}],
	  "channels": {"1": [
	    {"id": "10", "guild_id": "1", "name": "general", "type": 0},
	    {"id": "11", "guild_id": "1", "name": "shared", "type": 0}
	  ]},
	  "dms": [{"id": "20", "type": 1, "recipients": [{"id": "7", "username": "mina"}]}],
	  "users": {"1": [{"id": "7", "username": "mina"}, {"id": "900", "username": "jun"}]},
	  "messages": {
	    "10": [` + first + `,
	      {"id": "101", "channel_id": "10", "author": {"id": "900", "username": "jun"}, "content": "hi", "timestamp": "2026-10-24T09:05:00.000Z"}],
	    "20": [{"id": "200", "channel_id": "20", "author": {"id": "7", "username": "mina"}, "content": "dm", "timestamp": "2026-10-24T09:01:00.000Z"}]
	  }
	}`
}

func writeDiscordScript(t *testing.T, dir, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "discord.json"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestM4DiscordPersonalReadsThroughRunningCore proves the person's Discord
// account is read by the running Core: guild channels and direct messages page
// through the add-on, an edit becomes a new revision, and a channel the bot
// source already reads is left alone.
func TestM4DiscordPersonalReadsThroughRunningCore(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	program, err := filepath.Abs("../../adapters/messenger/discord-fixture-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	root := discordRoot(t)
	dir := addon.DataDir(root, discorduser.Platform, "900")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeDiscordScript(t, dir, discordScript(false))
	stop := startAddonCore(t, root, addon.Config{Node: node, Program: program, StateRoot: root,
		Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5,
		RequestTimeout: 30 * time.Second}, nil)
	defer func() { stop() }()

	waitFor(t, "the guild channel and the DM", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record WHERE account='900'`) == 3
	})
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_chat WHERE account='900'`); got != 2 {
		t.Fatalf("chats read by the personal account = %d, want 2 (the bot source keeps the shared one)", got)
	}
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_chat WHERE account='900' AND chat='11'`); got != 0 {
		t.Fatal("the shared channel was read twice")
	}
	if author := readString(t, root, `SELECT author FROM messenger_record WHERE chat='10' AND message='101'`); author != "900" {
		t.Fatalf("own message author = %q", author)
	}

	// The next sync reads the window again, so the edit arrives as a revision.
	writeDiscordScript(t, dir, discordScript(true))
	waitFor(t, "the edit as a second revision", func() bool {
		return readCanon(t, root, `SELECT count(*) FROM messenger_record_revision WHERE chat='10' AND message='100'`) == 2
	})
	if got := readCanon(t, root, `SELECT revision FROM messenger_record WHERE chat='10' AND message='100'`); got != 2 {
		t.Fatalf("stored revision = %d, want 2", got)
	}
	if state := readString(t, root, `SELECT state FROM messenger_source WHERE account='900'`); state != "connected" {
		t.Fatalf("source state = %q", state)
	}
}

// TestM4DiscordPersonalRevokedSession proves a revoked session stops that source
// at needs sign-in without reading or failing Core.
func TestM4DiscordPersonalRevokedSession(t *testing.T) {
	t.Parallel()
	requireAddonSandbox(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	program, err := filepath.Abs("../../adapters/messenger/discord-fixture-main.ts")
	if err != nil {
		t.Fatal(err)
	}
	root := discordRoot(t)
	dir := addon.DataDir(root, discorduser.Platform, "900")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	revoked := map[string]any{"token": "stale-token", "revoked": true,
		"me": map[string]any{"id": "900", "username": "jun"}, "servers": []any{},
		"messages": map[string]any{"dm-1": []any{}}}
	encoded, err := json.Marshal(revoked)
	if err != nil {
		t.Fatal(err)
	}
	writeDiscordScript(t, dir, string(encoded))
	stop := startAddonCore(t, root, addon.Config{Node: node, Program: program, StateRoot: root,
		Socket: filepath.Join(root, "core.sock"), RestartDelay: 20 * time.Millisecond, MaxRestarts: 5,
		RequestTimeout: 30 * time.Second}, nil)
	defer func() { stop() }()

	waitFor(t, "the source at needs sign-in", func() bool {
		return readString(t, root, `SELECT state FROM messenger_source WHERE account='900'`) == "needs_sign_in"
	})
	if got := readCanon(t, root, `SELECT count(*) FROM messenger_record`); got != 0 {
		t.Fatalf("records read from a revoked session = %d", got)
	}
}

// discordRoot prepares a headless root whose canon holds the person's Discord
// sources, as connecting them did before Core started: the personal account with
// Discord's terms accepted, and the bot source that already reads one channel.
func discordRoot(t *testing.T) string {
	t.Helper()
	const person = "person:discord"
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(root, "system", "core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system", "core", "main.json"), []byte(`{"main_id":"discord-personal"}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root)
	write := func(store *messenger.Store) {
		bot := messenger.Source{Person: person, Platform: discorduser.Platform, Account: "bot-1", Adapter: "discord",
			Mode: messenger.ModeOfficial, State: messenger.StateConnected, ConnectedAt: time.Now()}
		if err := store.Connect(t.Context(), bot); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertChats(t.Context(), bot, []messenger.Chat{{ID: "11", Name: "Guild A #shared", Kind: "channel"}}); err != nil {
			t.Fatal(err)
		}
		personal := discorduser.Source(person, "900", time.Now())
		personal.State = messenger.StateConnected
		if err := store.Connect(t.Context(), personal); err != nil {
			t.Fatal(err)
		}
	}
	withCanon(t, root, write)
	return root
}

// withCanon opens a prepared root's canon for writing and closes it again.
func withCanon(t *testing.T, root string, fn func(*messenger.Store)) {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := controls.MintEpoch(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: epoch,
		Controls: controls, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	fn(messenger.Open(db, nil, nil))
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
