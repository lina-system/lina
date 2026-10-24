// SPDX-License-Identifier: Apache-2.0
package discorduser_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/discorduser"
)

type fakeAdapter struct {
	chats []messenger.Chat
	err   error
}

func (f *fakeAdapter) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	return messenger.SignedIn, nil
}
func (f *fakeAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return f.chats, f.err
}
func (f *fakeAdapter) Page(context.Context, messenger.SourceID, string, messenger.Cursor) (messenger.Page, error) {
	return messenger.Page{}, nil
}
func (f *fakeAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (f *fakeAdapter) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (f *fakeAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, messenger.ErrInvalid
}
func (*fakeAdapter) Capabilities() messenger.Capabilities { return messenger.Capabilities{} }

func TestConnectCardCitesDiscordsGuidelineAndReadsOnly(t *testing.T) {
	card := discorduser.ConnectCard()
	if card.Platform != "discord" || card.Title == "" {
		t.Fatalf("card: %+v", card)
	}
	if card.Guideline != discorduser.GuidelineURL {
		t.Fatalf("guideline link: %q", card.Guideline)
	}
	for _, want := range []string{"self-bot", "only read", "QR"} {
		if !strings.Contains(card.Body, want) {
			t.Fatalf("card body does not say %q: %q", want, card.Body)
		}
	}
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	source := discorduser.Source("person", "900", at)
	if source.Platform != discorduser.Platform || source.Mode != messenger.ModeUnofficial || source.Adapter != discorduser.Adapter {
		t.Fatalf("source: %+v", source)
	}
	if source.AcceptedTerms == nil || source.AcceptedTerms.Person != "person" || !source.AcceptedTerms.At.Equal(at) {
		t.Fatalf("acceptance: %+v", source.AcceptedTerms)
	}
	if !strings.Contains(source.AcceptedTerms.Text, discorduser.GuidelineURL) {
		t.Fatalf("accepted term does not cite the guideline: %q", source.AcceptedTerms.Text)
	}
}

func TestOnceLeavesOutAChannelABotSourceAlreadyReads(t *testing.T) {
	adapter := &fakeAdapter{chats: []messenger.Chat{
		{ID: "10", Name: "Guild A #general", Kind: "channel"},
		{ID: "11", Name: "Guild A #private", Kind: "channel"},
		{ID: "20", Name: "mina", Kind: "direct"},
	}}
	bot := discorduser.BotChats(func(context.Context) (map[string]bool, error) {
		return map[string]bool{"10": true}, nil
	})
	once := discorduser.Once(adapter, bot)
	chats, err := once.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: "900"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ID != "11" || chats[1].ID != "20" {
		t.Fatalf("chats read once: %+v", chats)
	}
	if len(adapter.chats) != 3 {
		t.Fatal("the adapter's own list changed")
	}
}

func TestOnceReadsEverythingWhenNoBotSourceReadsIt(t *testing.T) {
	adapter := &fakeAdapter{chats: []messenger.Chat{{ID: "10", Kind: "channel"}}}
	once := discorduser.Once(adapter, func(context.Context) (map[string]bool, error) { return nil, nil })
	chats, err := once.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: "900"})
	if err != nil || len(chats) != 1 {
		t.Fatalf("chats: %+v %v", chats, err)
	}
}

func TestOnceReportsTheOverlapFailureInsteadOfReadingTwice(t *testing.T) {
	adapter := &fakeAdapter{chats: []messenger.Chat{{ID: "10", Kind: "channel"}}}
	failure := errors.New("registry unavailable")
	once := discorduser.Once(adapter, func(context.Context) (map[string]bool, error) { return nil, failure })
	if _, err := once.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: "900"}); !errors.Is(err, failure) {
		t.Fatalf("error: %v", err)
	}
}

func TestOnceKeepsTheAdapterBoundary(t *testing.T) {
	adapter := &fakeAdapter{err: messenger.ErrNeedsSignIn}
	once := discorduser.Once(adapter, func(context.Context) (map[string]bool, error) {
		t.Fatal("overlap read without a signed-in account")
		return nil, nil
	})
	if _, err := once.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: "900"}); !errors.Is(err, messenger.ErrNeedsSignIn) {
		t.Fatalf("error: %v", err)
	}
}
