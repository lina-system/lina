// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/internal/core/messenger/kakaotalk"
	"github.com/thisisjun786/lina/kit/protocol"
)

// kakaoRuntime is a control runtime whose KakaoTalk source runs over an add-on
// manager with no Node.js or program: the card and the source list need neither.
func kakaoRuntime(t *testing.T) *conversationRuntime {
	t.Helper()
	r := controlRuntime(t)
	manager := addon.NewManager(addon.Config{StateRoot: t.TempDir()})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	initKakaoTalk(r, &MessengerConfig{Addon: manager})
	if r.kakao == nil {
		t.Fatal("KakaoTalk source not composed")
	}
	return r
}

func TestKakaoTalkControlRequiresTheOwnerDeciderAndCurrentEpoch(t *testing.T) {
	r := kakaoRuntime(t)
	op := r.identity.Operations[protocol.KindKakaotalkRequest]
	if op.Handle == nil || op.Disconnect != nil {
		t.Fatal("KakaoTalk not registered, or it took a disconnect hook")
	}
	list := protocol.KakaotalkRequest{Operation: "list"}
	for _, s := range []clientsrv.Session{
		{PersonID: "other", Epoch: r.epoch, CanDecide: true},
		{PersonID: r.person, Epoch: r.epoch},
		{PersonID: r.person, Epoch: r.epoch + 1, CanDecide: true},
	} {
		_, err := op.Handle(t.Context(), s, protocol.Envelope{Payload: list})
		var refused *clientsrv.OperationError
		if !errors.As(err, &refused) || refused.Code != protocol.ErrorNotAuthorized {
			t.Fatalf("owner boundary bypass for %+v: %v", s, err)
		}
	}
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	result, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: list})
	if err != nil {
		t.Fatal(err)
	}
	sources, ok := result.Payload.(protocol.KakaotalkResult)
	if !ok || sources.Operation == nil || *sources.Operation != "list" || sources.Sources == nil || len(*sources.Sources) != 0 {
		t.Fatalf("source list %+v", result.Payload)
	}
}

// An install without the add-on answers as unavailable rather than pretending
// to have a source.
func TestKakaoTalkWithoutTheAddonIsUnavailable(t *testing.T) {
	r := controlRuntime(t)
	op := r.identity.Operations[protocol.KindKakaotalkRequest]
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	_, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: protocol.KakaotalkRequest{Operation: "list"}})
	var refused *clientsrv.OperationError
	if !errors.As(err, &refused) || refused.Code != protocol.ErrorUnavailable {
		t.Fatalf("unconfigured add-on: %v", err)
	}
}

// The card names the cited terms, and an accepted connect is the only way in.
func TestKakaoTalkCardCitesTheTermsAndConnectNeedsAcceptance(t *testing.T) {
	r := kakaoRuntime(t)
	op := r.identity.Operations[protocol.KindKakaotalkRequest]
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	result, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: protocol.KakaotalkRequest{Operation: "card"}})
	if err != nil {
		t.Fatal(err)
	}
	card := result.Payload.(protocol.KakaotalkResult)
	if card.Terms == nil || card.Links == nil || len(*card.Links) != len(kakaotalk.Links) {
		t.Fatalf("card %+v", card)
	}
	if !strings.Contains(*card.Terms, kakaotalk.Links[0]) || !strings.Contains(*card.Terms, "never sends") {
		t.Fatalf("card terms do not state the risk: %s", *card.Terms)
	}
	if result.Effect != protocol.EffectNone {
		t.Fatalf("opening a card was recorded as an effect: %q", result.Effect)
	}
	_, err = op.Handle(t.Context(), owner, protocol.Envelope{Payload: protocol.KakaotalkRequest{Operation: "connect"}})
	var refused *clientsrv.OperationError
	if !errors.As(err, &refused) || refused.Code != protocol.ErrorMalformed {
		t.Fatalf("connect without acceptance: %v", err)
	}
	for _, request := range []protocol.KakaotalkRequest{
		{Operation: ""},
		{Operation: "sign_in"},
		{Operation: "answer", Prompt: str("email")},
		{Operation: "disconnect"},
		{Operation: "unknown"},
		{Operation: "list", Platform: str("discord")},
		{Operation: "list", Accept: new(true)},
		{Operation: "card", Account: str("7788")},
	} {
		_, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: request})
		if !errors.As(err, &refused) || refused.Code != protocol.ErrorMalformed {
			t.Fatalf("request %+v accepted: %v", request, err)
		}
	}
}

// A recorded gap is told through the ordinary background notice, naming the
// chat and the platform, and remains in history as a system notice.
func TestMessengerGapNoticeNamesTheChatAndPlatform(t *testing.T) {
	r := kakaoRuntime(t)
	store := messenger.Open(r.db, r.materials, r.removals)
	source := messenger.Source{Person: r.person, Platform: kakaotalk.Platform, Account: "7788", Mode: messenger.ModeUnofficial,
		Adapter: kakaotalk.AdapterName, State: messenger.StateConnected}
	chat := messenger.ChatRecord{Chat: "chat-1", Name: "A friend", Kind: "direct"}
	service := messenger.NewService(store, nil, time.Minute)
	service.Notify = r.notifyMessengerGap
	fresh, err := store.NoteGap(t.Context(), source, chat.Chat)
	if err != nil || !fresh {
		t.Fatalf("record gap: %v %v", fresh, err)
	}
	if err := service.Notify(t.Context(), source, chat); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE conversation_id=? AND json_extract(origin,'$.locator') LIKE 'background-notice:%'", r.conversation).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("gap notice events = %d, %v", count, err)
	}
	var body string
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT body FROM conv_event WHERE conversation_id=? AND json_extract(origin,'$.locator') LIKE 'background-notice:%'", r.conversation).Scan(&body)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "A friend") || !strings.Contains(body, kakaotalk.Platform) {
		t.Fatalf("the notice does not name the chat and the platform: %s", body)
	}
}
