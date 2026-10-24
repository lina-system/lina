// SPDX-License-Identifier: Apache-2.0
package telegram_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
)

type recoveryReader interface {
	Recover(context.Context, messenger.SourceID, func(messenger.Event) error) error
}

func TestRecoveryReplaysOfflineUpdatesAndCheckpointsAfterAdmission(t *testing.T) {
	s := newServer(t)
	vault := newVault(t)
	var mu sync.Mutex
	var common, channel []int
	s.dispatcher.HandleFunc(tg.UpdatesGetStateRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.UpdatesState{Pts: 10, Date: 100, Seq: 1})
	})
	group := &tg.Channel{ID: 3003, Title: "Channel", Broadcast: true, Photo: &tg.ChatPhotoEmpty{}}
	group.SetAccessHash(33)
	s.dispatcher.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		dialog := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 3003}, TopMessage: 3}
		dialog.SetPts(10)
		return server.SendResult(req, &tg.MessagesDialogs{Dialogs: []tg.DialogClass{dialog, &tg.Dialog{Peer: &tg.PeerUser{UserID: other.ID}, TopMessage: 3}}, Users: []tg.UserClass{self, other}, Chats: []tg.ChatClass{group}})
	})
	edited := &tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 200, EditDate: 500, Message: "offline edit"}
	s.dispatcher.HandleFunc(tg.UpdatesGetDifferenceRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.UpdatesGetDifferenceRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		mu.Lock()
		common = append(common, request.Pts)
		mu.Unlock()
		switch request.Pts {
		case 10:
			return server.SendResult(req, &tg.UpdatesDifferenceSlice{OtherUpdates: []tg.UpdateClass{&tg.UpdateEditMessage{Message: edited, Pts: 11, PtsCount: 1}}, Users: []tg.UserClass{self, other}, IntermediateState: tg.UpdatesState{Pts: 11, Date: 101, Seq: 2}})
		case 11:
			return server.SendResult(req, &tg.UpdatesDifference{OtherUpdates: []tg.UpdateClass{&tg.UpdateDeleteMessages{Messages: []int{1}, Pts: 12, PtsCount: 1}}, State: tg.UpdatesState{Pts: 12, Date: 102, Seq: 3}})
		default:
			return server.SendResult(req, &tg.UpdatesDifferenceEmpty{Date: 103, Seq: 3})
		}
	})
	s.dispatcher.HandleFunc(tg.UpdatesGetChannelDifferenceRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.UpdatesGetChannelDifferenceRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		mu.Lock()
		channel = append(channel, request.Pts)
		mu.Unlock()
		if request.Pts == 10 {
			msg := *edited
			msg.PeerID = &tg.PeerChannel{ChannelID: 3003}
			return server.SendResult(req, &tg.UpdatesChannelDifference{Final: true, Pts: 12, Chats: []tg.ChatClass{group}, Users: []tg.UserClass{other}, OtherUpdates: []tg.UpdateClass{&tg.UpdateEditChannelMessage{Message: &msg, Pts: 11, PtsCount: 1}, &tg.UpdateDeleteChannelMessages{ChannelID: 3003, Messages: []int{1}, Pts: 12, PtsCount: 1}}})
		}
		return server.SendResult(req, &tg.UpdatesChannelDifferenceEmpty{Final: true, Pts: 12})
	})
	first := newAdapter(t, s, vault, nil)
	signIn(t, first)
	if _, err := first.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	recoverer, ok := any(first).(recoveryReader)
	if !ok {
		t.Fatal("Telegram has no persistent update recovery")
	}
	if err := recoverer.Recover(t.Context(), sourceID, func(messenger.Event) error { t.Fatal("new baseline replayed history"); return nil }); err != nil {
		t.Fatal(err)
	}
	first.Close()
	failure := errors.New("admission unavailable")
	second := newAdapter(t, s, vault, nil)
	if _, err := second.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	if err := any(second).(recoveryReader).Recover(t.Context(), sourceID, func(messenger.Event) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("admission failure lost: %v", err)
	}
	second.Close()
	third := newAdapter(t, s, vault, nil)
	if _, err := third.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	var events []messenger.Event
	if err := any(third).(recoveryReader).Recover(t.Context(), sourceID, func(e messenger.Event) error { events = append(events, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Message == nil || events[0].Message.Text != "offline edit" || events[1].Deletion == nil || events[2].Chat != "channel:3003" || events[3].Deletion == nil {
		t.Fatalf("recovered events=%+v", events)
	}
	third.Close()
	fourth := newAdapter(t, s, vault, nil)
	if _, err := fourth.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	if err := any(fourth).(recoveryReader).Recover(t.Context(), sourceID, func(messenger.Event) error { t.Fatal("committed update replayed"); return nil }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(common) != 4 || common[0] != 10 || common[1] != 10 || common[2] != 11 || common[3] != 12 || len(channel) != 2 || channel[0] != 10 || channel[1] != 12 {
		t.Fatalf("checkpoints: common=%v channel=%v", common, channel)
	}
}

func TestTooLongRecoveryRetainsTheLastCheckpoint(t *testing.T) {
	s := newServer(t)
	a := newAdapter(t, s, newVault(t), nil)
	signIn(t, a)
	if _, err := a.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	s.dispatcher.HandleFunc(tg.UpdatesGetStateRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.UpdatesState{Pts: 10, Date: 100})
	})
	var mu sync.Mutex
	var requested []int
	s.dispatcher.HandleFunc(tg.UpdatesGetDifferenceRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.UpdatesGetDifferenceRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		mu.Lock()
		requested = append(requested, request.Pts)
		mu.Unlock()
		return server.SendResult(req, &tg.UpdatesDifferenceTooLong{Pts: 999})
	})
	deliver := func(messenger.Event) error { t.Fatal("too-long response invented an event"); return nil }
	if err := a.Recover(t.Context(), sourceID, deliver); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.Recover(t.Context(), sourceID, deliver); !errors.Is(err, telegram.ErrUpdateGap) {
			t.Fatalf("gap not reported: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requested) != 2 || requested[0] != 10 || requested[1] != 10 {
		t.Fatalf("gap advanced checkpoint: %v", requested)
	}
}
