// SPDX-License-Identifier: Apache-2.0

// Package whatsapp carries what WhatsApp itself states for a linked-device
// read: what history the phone pushed, that no edit or deletion signal exists,
// and how a refusal is told apart from every other failure.
//
// The reads run in the messenger add-on. This package narrows the add-on's
// adapter for a WhatsApp source, so the capability descriptor and the failing
// cause name WhatsApp rather than the add-on ([integrations.md#platforms]).
//
// [integrations.md#platforms]: ../../../../docs/design/integrations.md#platforms
package whatsapp

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
)

// Platform is WhatsApp's name in the source registry.
const Platform = "whatsapp"

// ErrForbidden is a refusal: WhatsApp restricted the account, so the source
// fails with that cause and reading resumes only after the person acts on the
// phone.
var ErrForbidden = errors.New("WhatsApp restricted this account")

// Capabilities states what a linked device can read. History is only what the
// phone pushed when it linked; nothing older is reachable later. Upstream holds
// at most 500 messages per chat for the running add-on, so a message sent while
// the add-on was down is missed: the read window is two intervals, not a day.
// The SDK reports no edit or deletion signal.
func Capabilities() messenger.Capabilities {
	return messenger.Capabilities{
		HistoryDepth: "only what the phone pushed at link time",
		CursorKind:   "position",
		Edits:        false,
		Deletions:    false,
		LiveEvents:   true,
		Interval:     15 * time.Minute,
		ReadWindow:   30 * time.Minute,
	}
}

// Adapter is the add-on adapter for one WhatsApp account with WhatsApp's
// capabilities and refusal in front of it.
type Adapter struct {
	inner messenger.Adapter
}

// Wrap narrows an add-on adapter for one WhatsApp source.
func Wrap(inner messenger.Adapter) *Adapter { return &Adapter{inner: inner} }

// Capabilities returns WhatsApp's, not the add-on's platform-wide set.
func (a *Adapter) Capabilities() messenger.Capabilities { return Capabilities() }

func (a *Adapter) SignIn(ctx context.Context, id messenger.SourceID) (messenger.SignInState, error) {
	state, err := a.inner.SignIn(ctx, id)
	return state, refusal(err)
}

func (a *Adapter) ListChats(ctx context.Context, id messenger.SourceID) ([]messenger.Chat, error) {
	chats, err := a.inner.ListChats(ctx, id)
	return chats, refusal(err)
}

func (a *Adapter) Page(ctx context.Context, id messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	page, err := a.inner.Page(ctx, id, chat, cursor)
	return page, refusal(err)
}

func (a *Adapter) Members(ctx context.Context, id messenger.SourceID, chat string) ([]messenger.Author, error) {
	members, err := a.inner.Members(ctx, id, chat)
	return members, refusal(err)
}

func (a *Adapter) Live(ctx context.Context, id messenger.SourceID, deliver func(messenger.Event) error) error {
	return refusal(a.inner.Live(ctx, id, deliver))
}

func (a *Adapter) FetchAttachment(ctx context.Context, id messenger.SourceID, chat, message string) (io.ReadCloser, error) {
	body, err := a.inner.FetchAttachment(ctx, id, chat, message)
	return body, refusal(err)
}

// refusal keeps the platform's refusal as its own error: a live event without a
// chat is LINA's own invalid event, and every other cause passes through.
func refusal(err error) error {
	if err == nil || errors.Is(err, messenger.ErrInvalid) {
		return err
	}
	// The add-on reports a platform refusal as its cause code, so only the
	// forbidden code turns into a restriction on this account.
	if errors.Is(err, addon.ErrRefused) && strings.HasSuffix(err.Error(), "source_forbidden") {
		return ErrForbidden
	}
	return err
}
