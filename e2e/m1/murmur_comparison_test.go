// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

type comparisonAdapter struct{ *scriptedAdapter }

func (a *comparisonAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "123", Kind: "direct"}}, nil
}

// The running Core, not the test, admits the source's messages. The real CLI
// compares that live canon through a read-only connection without stopping it.
func TestM1MurmurComparisonRunningCore(t *testing.T) {
	t.Parallel()
	h := newHarness(t, buildLina(t))
	installMessengerBundle(t, h)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: "person:messenger-host", Platform: "kakaotalk", Account: "7788", Mode: messenger.ModeUnofficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start, AcceptedTerms: &messenger.Terms{Person: "person:messenger-host", At: start, Text: "synthetic"}}
	db, closeStore := installedAcceptanceStore(t, h)
	if err := messenger.Open(db, nil, nil).Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	closeStore()
	adapter := &comparisonAdapter{&scriptedAdapter{pages: map[string][]scriptedPage{
		"":         {{page: messenger.Page{Messages: []messenger.Message{message("9223372036854775807", messenger.RoutePerson, start, "synthetic search phrase"), message("101", messenger.RouteParticipant, start.Add(time.Hour), "synthetic reply")}, Next: "complete", HasMore: true}}},
		"complete": {{block: true}},
	}}}
	stop := runMessengerCore(t, h, adapter)
	defer stop()
	adapter.waitFor(t, "complete")
	inbox := t.TempDir()
	file := filepath.Join(inbox, "123.jsonl")
	line := func(id string, at time.Time) []byte {
		raw, err := json.Marshal(map[string]any{"type": "message", "source": "agent-messenger", "message_id": id, "timestamp": at.Format(time.RFC3339Nano), "sender": map[string]string{"id": "999", "name": "PRIVATE NAME"}, "text": "PRIVATE BODY", "message_type": "kakao-1"})
		if err != nil {
			t.Fatal(err)
		}
		return append(raw, '\n')
	}
	identical := append(line("123-9223372036854775807", start), line("101", start.Add(time.Hour))...)
	run := func(body []byte, want int) messenger.Comparison {
		t.Helper()
		if err := os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
		p := h.command("compare-murmur", "--state-root", h.root, "--person", source.Person, "--account", source.Account, "--inbox", inbox, "--since", start.Format(time.RFC3339), "--until", start.Add(7*24*time.Hour).Format(time.RFC3339))
		p.start(t)
		p.wait(t, want)
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(body, after) {
			t.Fatal("comparison changed inbox")
		}
		if bytes.Contains(p.stdout.Bytes(), []byte("PRIVATE")) || bytes.Contains(p.stderr.Bytes(), []byte("PRIVATE")) {
			t.Fatal("comparison leaked content")
		}
		if want == 65 {
			if p.stdout.Len() != 0 {
				t.Fatal("refused inbox produced partial report")
			}
			return messenger.Comparison{}
		}
		var report messenger.Comparison
		if err := json.Unmarshal(p.stdout.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	same := run(identical, 0)
	if same.Murmur != 2 || same.LINA != 2 || same.MissingLINA != 0 || same.MissingMurmur != 0 {
		t.Fatalf("identical report: %+v", same)
	}
	missing := run(append(append([]byte{}, identical...), line("102", start.Add(2*time.Hour))...), 1)
	if missing.MissingLINA != 1 || missing.Chats[0].MissingLINA[0] != "102" {
		t.Fatalf("missing report: %+v", missing)
	}
	reverse := run(line("101", start.Add(time.Hour)), 0)
	if reverse.MissingMurmur != 1 || reverse.Chats[0].MissingMurmur[0] != "9223372036854775807" {
		t.Fatalf("reverse report: %+v", reverse)
	}
	run(append(identical, []byte("{PRIVATE")...), 65)
	run(identical, 0)
}
