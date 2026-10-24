// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// The installed command observes records admitted by the running Core's source
// loop, then observes the same keys beside the restarted installed Core binary.
func TestM1MurmurCompareRunningCore(t *testing.T) {
	t.Parallel()
	h, start := installedConversationHarnessWithin(t, 90*time.Second, "")
	core := start(false)
	h.stopCore(core)
	person := installedPerson(t, h)
	home, _ := murmurHome(t)
	before := importSourceSnapshot(t, home)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: person, Platform: "kakaotalk", Account: "kakao-1", Adapter: "fixture", Mode: messenger.ModeUnofficial, State: messenger.StateConnected, ConnectedAt: at,
		AcceptedTerms: &messenger.Terms{Person: person, At: at, Text: "synthetic acceptance"}}
	db, closeStore := installedAcceptanceStore(t, h)
	if err := messenger.Open(db, nil, nil).Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	closeStore()
	adapter := &comparisonAdapter{at: at, first: make(chan struct{}), release: make(chan struct{}), second: make(chan struct{})}
	stop := runMessengerCore(t, h, adapter)
	var stopOnce sync.Once
	t.Cleanup(func() { stopOnce.Do(stop) })
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-h.ctx.Done():
			t.Fatal("source did not reach its committed cursor")
		}
	}
	wait(adapter.first)
	args := []string{"compare-murmur", "--state-root", h.root, "--inbox", filepath.Join(home, "spool/events.jsonl"), "--person", person, "--account", source.Account, "--since", at.Format(time.RFC3339), "--until", at.Add(7 * 24 * time.Hour).Format(time.RFC3339)}
	compare := func() messenger.Comparison {
		t.Helper()
		p := h.command(args...)
		p.start(t)
		p.wait(t, 0)
		var result messenger.Comparison
		if err := json.Unmarshal(p.stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"archive text", "live text", "hello", "Friend"} {
			if strings.Contains(p.stdout.String()+p.stderr.String(), text) {
				t.Fatal("comparison exposed content")
			}
		}
		return result
	}
	missing := compare()
	if missing.MurmurCount != 2 || missing.LinaCount != 1 || len(missing.Chats) != 1 || !reflect.DeepEqual(missing.Chats[0].MissingInLina, []string{"101"}) {
		t.Fatalf("missing key: %+v", missing)
	}
	close(adapter.release)
	wait(adapter.second)
	matched := compare()
	if matched.MurmurCount != 2 || matched.LinaCount != 2 || len(matched.Chats) != 1 || len(matched.Chats[0].MissingInLina)+len(matched.Chats[0].MissingInMurmur) != 0 {
		t.Fatalf("identical: %+v", matched)
	}
	stopOnce.Do(stop)
	core = start(false)
	if got := compare(); !reflect.DeepEqual(got, matched) {
		t.Fatalf("restart: %+v", got)
	}
	h.stopCore(core)
	if after := importSourceSnapshot(t, home); !reflectEqualMap(before, after) {
		t.Fatal("comparison changed the inbox")
	}
	bad := filepath.Join(t.TempDir(), "inbox.jsonl")
	if err := os.WriteFile(bad, []byte("{\"private-body\":"), 0600); err != nil {
		t.Fatal(err)
	}
	badArgs := append([]string{}, args...)
	badArgs[4] = bad
	p := h.command(badArgs...)
	p.start(t)
	p.wait(t, 78)
	if p.stdout.Len() != 0 || strings.Contains(p.stderr.String(), "private-body") {
		t.Fatal("bad inbox leaked content or emitted partial success")
	}
}

type comparisonAdapter struct {
	scriptedAdapter
	at                     time.Time
	first, release, second chan struct{}
	firstOnce, secondOnce  sync.Once
}

func (a *comparisonAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "chat-1", Kind: "direct"}}, nil
}

func (a *comparisonAdapter) Page(ctx context.Context, _ messenger.SourceID, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	switch cursor {
	case "":
		return messenger.Page{Messages: []messenger.Message{message("100", messenger.RoutePerson, a.at, "live text")}, Next: "first", HasMore: true}, nil
	case "first":
		a.firstOnce.Do(func() { close(a.first) })
		select {
		case <-a.release:
		case <-ctx.Done():
			return messenger.Page{}, ctx.Err()
		}
		return messenger.Page{Messages: []messenger.Message{message("101", messenger.RouteParticipant, a.at.Add(time.Minute), "hello")}, Next: "second", HasMore: true}, nil
	default:
		a.secondOnce.Do(func() { close(a.second) })
		<-ctx.Done()
		return messenger.Page{}, ctx.Err()
	}
}
