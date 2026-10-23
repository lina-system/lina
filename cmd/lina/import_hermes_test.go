// SPDX-License-Identifier: Apache-2.0
package main

import (
	"github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestImportHermesCommandRegistered(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	if out, err := exec.Command(binary, "import-hermes", "--help").CombinedOutput(); err != nil {
		t.Fatalf("installed command missing: %v %s", err, out)
	}
}

func TestConfirmedHermesOwners(t *testing.T) {
	candidates := []hermes.OwnerChannelCandidate{
		{OwnerChannel: hermes.OwnerChannel{Source: "telegram", SessionID: "a", UserID: "user", ChatID: "chat"}, ChatType: "private"},
		{OwnerChannel: hermes.OwnerChannel{Source: "telegram", SessionID: "b", UserID: "user", ChatID: "chat"}, ChatType: "dm"},
		{OwnerChannel: hermes.OwnerChannel{Source: "telegram", SessionID: "group", UserID: "user", ChatID: "chat"}, ChatType: "group"},
		{OwnerChannel: hermes.OwnerChannel{Source: "telegram", SessionID: "other", UserID: "user", ChatID: "other"}, ChatType: "private"},
		{OwnerChannel: hermes.OwnerChannel{Source: "desktop", SessionID: "desktop", UserID: "basic:owner"}},
		{OwnerChannel: hermes.OwnerChannel{Source: "webui", SessionID: "web"}},
		{OwnerChannel: hermes.OwnerChannel{Source: "cli", SessionID: "cli"}},
	}
	got, err := confirmedHermesOwners(candidates, "desktop,web,cli", []string{"telegram:user:chat", "telegram:user:chat"})
	if err != nil {
		t.Fatal(err)
	}
	want := []hermes.OwnerChannel{candidates[4].OwnerChannel, candidates[5].OwnerChannel, candidates[6].OwnerChannel, candidates[0].OwnerChannel, candidates[1].OwnerChannel}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owners=%+v, want %+v", got, want)
	}
	for _, tc := range []struct{ session, channel string }{
		{"a", ""}, {"missing", ""}, {"", "telegram:user"}, {"", "telegram::chat"}, {"", "telegram:user:"}, {"", "desktop:user:chat"}, {"", "cron:user:chat"}, {"", "telegram:wrong:chat"}, {"", "telegram:user:missing"},
	} {
		t.Run(tc.session+tc.channel, func(t *testing.T) {
			var channels []string
			if tc.channel != "" {
				channels = []string{tc.channel}
			}
			if _, err := confirmedHermesOwners(candidates, tc.session, channels); err == nil {
				t.Fatal("unmatched or invalid confirmation accepted")
			}
		})
	}
	if _, err := confirmedHermesOwners(candidates[2:3], "", []string{"telegram:user:chat"}); err == nil {
		t.Fatal("shared chat confirmed")
	}
}
