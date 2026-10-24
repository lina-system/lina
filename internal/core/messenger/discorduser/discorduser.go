// SPDX-License-Identifier: Apache-2.0

// Package discorduser is LINA's contract for the person's own Discord account,
// read through the messenger add-on ([docs/design/integrations.md#platforms]).
// Discord's terms forbid self-bots, so this source reads only and signs in only
// by scanning a QR code with the person's Discord mobile app. The add-on carries
// the operations; this package holds what Core and the connection surface share:
// the connect card the person accepts, the source they connect, and the rule
// that a chat a bot-mode source already reads is left to that source.
//
// The personal account and LINA's bot on the same platform are two adapters; a
// chat read by both would otherwise hold two records of one message. The
// personal source lists only chats no bot-mode source reads, so each chat is
// read once and its message ids keep one record identity.
package discorduser

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

const (
	// Platform is Discord, whose two ways in (bot and personal) are separate
	// adapters of one platform.
	Platform = "discord"
	// Adapter is the name a source of the add-on records.
	Adapter = messenger.AddonAdapter
	// GuidelineURL is the term the connect card cites.
	GuidelineURL = "https://discord.com/guidelines/"
)

// Card is the connect card the person reads before connecting. It says in plain
// words what the terms forbid, links the term, and says that LINA will only
// read. The person's acceptance is recorded with their id and the date.
type Card struct {
	Platform  string
	Title     string
	Body      string
	Guideline string
}

// ConnectCard is Discord's personal-account card.
func ConnectCard() Card {
	return Card{
		Platform: Platform,
		Title:    "Read your Discord messages",
		Body: "Discord's guidelines say \"Do not use self-bots or user-bots\", so this is " +
			"not an official way in. LINA will only read; it never sends, edits or marks anything read. " +
			"Even reading may get the account limited. You sign in by scanning a QR code " +
			"with the Discord app on your phone, and LINA never takes a token from the " +
			"desktop app or a browser.",
		Guideline: GuidelineURL,
	}
}

// Source is the source the person connects after accepting the card. Connecting
// is the person's act; nothing here discovers an account.
func Source(person, account string, at time.Time) messenger.Source {
	return messenger.Source{
		Person:   person,
		Platform: Platform,
		Account:  account,
		Mode:     messenger.ModeUnofficial,
		Adapter:  Adapter,
		State:    messenger.StateNeedsSignIn,
		AcceptedTerms: &messenger.Terms{
			Person: person,
			At:     at,
			Text: "Discord's terms forbid automating a personal account (" + GuidelineURL +
				"); even reading may get the account limited. LINA will only read. Signed in by QR code.",
		},
		ConnectedAt: at,
	}
}

// BotChats reports the chats a bot-mode source of the same platform already
// reads for this person.
type BotChats func(context.Context) (map[string]bool, error)

// Once returns the adapter that leaves every chat a bot-mode source already
// reads to that source. A failure to read the overlap set fails the listing
// instead of reading a chat twice.
func Once(adapter messenger.Adapter, bot BotChats) messenger.Adapter {
	return &once{Adapter: adapter, bot: bot}
}

type once struct {
	messenger.Adapter
	bot BotChats
}

var _ messenger.Adapter = (*once)(nil)

func (o *once) ListChats(ctx context.Context, id messenger.SourceID) ([]messenger.Chat, error) {
	// The adapter's own read comes first: a source that cannot sign in never
	// asks the registry which chats to skip.
	chats, err := o.Adapter.ListChats(ctx, id)
	if err != nil {
		return nil, err
	}
	if o.bot == nil {
		return chats, nil
	}
	read, err := o.bot(ctx)
	if err != nil {
		return nil, err
	}
	if len(read) == 0 {
		return chats, nil
	}
	kept := make([]messenger.Chat, 0, len(chats))
	for _, chat := range chats {
		if !read[chat.ID] {
			kept = append(kept, chat)
		}
	}
	return kept, nil
}
