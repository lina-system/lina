// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestCatchUpKeepsWatermarkUntilAllPagesArrive(t *testing.T) {
	a := &Adapter{size: 2, users: map[int64]*tg.User{}, chats: map[int64]*tg.Chat{}, channels: map[int64]*tg.Channel{}, attachments: map[string]tg.InputFileLocationClass{}}
	p := position{newest: 5, oldest: 1, phase: phaseSteady}
	var ids []string
	for _, pair := range [][2]int{{10, 9}, {8, 7}, {6, 5}} {
		var raw []*tg.Message
		for _, id := range pair {
			raw = append(raw, &tg.Message{ID: id, Date: id, Message: "new", PeerID: &tg.PeerUser{UserID: 42}, FromID: &tg.PeerUser{UserID: 42}})
		}
		page := a.steadyPage(raw, p)
		for _, m := range page.Messages {
			ids = append(ids, m.ID)
		}
		p = decode(string(page.Next))
	}
	if len(ids) != 5 || p.newest != 10 || p.before != 0 {
		t.Fatalf("catch-up lost messages: ids=%v position=%+v", ids, p)
	}
}

func TestServiceMessagesStillAdvanceHistory(t *testing.T) {
	a := &Adapter{size: 2}
	raw, err := a.history(&tg.MessagesMessages{Messages: []tg.MessageClass{&tg.MessageService{ID: 9, Date: 9, PeerID: &tg.PeerChat{ChatID: 2}}, &tg.MessageEmpty{ID: 8}}})
	if err != nil {
		t.Fatal(err)
	}
	page := a.fillPage(position{phase: phaseFill}, raw)
	p := decode(string(page.Next))
	if !page.HasMore || p.oldest != 8 || p.newest != 9 {
		t.Fatalf("service/empty entries truncated fill: %+v %+v", page, p)
	}
}
func TestBotAttributedOutgoingMessageIsNotPersonalMemory(t *testing.T) {
	a := &Adapter{}
	msg, _ := a.messageLocked(&tg.Message{ID: 1, Date: 1, Out: true, ViaBotID: 99, PeerID: &tg.PeerUser{UserID: 2}, FromID: &tg.PeerUser{UserID: 1}})
	if msg.Route != "automation" {
		t.Fatalf("bot-authored outgoing route=%s", msg.Route)
	}
}
