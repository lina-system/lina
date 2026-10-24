// SPDX-License-Identifier: Apache-2.0
package whatsapp_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/internal/core/messenger/whatsapp"
)

// inner stands in for the add-on adapter the supervisor returns.
type inner struct {
	messenger.Adapter
	signInErr error
	event     *messenger.Event
}

func (i inner) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	if i.signInErr != nil {
		return messenger.Failing, i.signInErr
	}
	return messenger.SignedIn, nil
}

func (i inner) Live(_ context.Context, _ messenger.SourceID, deliver func(messenger.Event) error) error {
	if i.event == nil {
		return nil
	}
	return deliver(*i.event)
}

func TestCapabilitiesStateWhatsAppReads(t *testing.T) {
	capabilities := whatsapp.Capabilities()
	if !strings.Contains(capabilities.HistoryDepth, "link time") || !capabilities.LiveEvents {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if capabilities.ReadWindow > 2*capabilities.Interval {
		t.Fatalf("a lost read window of %s understates the loss", capabilities.ReadWindow)
	}
	if capabilities.Edits || capabilities.Deletions || len(capabilities.Writes) != 0 {
		t.Fatalf("writes or signals WhatsApp does not report: %+v", capabilities)
	}
	if capabilities.Interval <= 0 || capabilities.ReadWindow <= 0 {
		t.Fatalf("coverage intervals = %+v", capabilities)
	}
}

func TestForbiddenAccountKeepsThatCause(t *testing.T) {
	adapter := whatsapp.Wrap(inner{signInErr: fmt.Errorf("%w: source_forbidden", addon.ErrRefused)})
	_, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: whatsapp.Platform, Account: "account-1"})
	if !errors.Is(err, whatsapp.ErrForbidden) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("cause is not plain: %q", err)
	}
}

func TestOtherFailuresPassThrough(t *testing.T) {
	failure := errors.New("messenger add-on stopped: request timed out")
	adapter := whatsapp.Wrap(inner{signInErr: failure})
	_, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: whatsapp.Platform, Account: "account-1"})
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
}

func TestOtherCauseCodesPassThrough(t *testing.T) {
	adapter := whatsapp.Wrap(inner{signInErr: fmt.Errorf("%w: source_logged_out", addon.ErrRefused)})
	_, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: whatsapp.Platform, Account: "account-1"})
	if errors.Is(err, whatsapp.ErrForbidden) || !errors.Is(err, addon.ErrRefused) {
		t.Fatalf("error = %v", err)
	}
}

func TestLiveEventReachesTheCallerWithItsChat(t *testing.T) {
	value := messenger.Message{ID: "m-1", Route: messenger.RouteParticipant, At: time.Now()}
	adapter := whatsapp.Wrap(inner{event: &messenger.Event{Chat: "15551234567@s.whatsapp.net", Message: &value}})
	var seen messenger.Event
	err := adapter.Live(t.Context(), messenger.SourceID{Platform: whatsapp.Platform, Account: "account-1"}, func(event messenger.Event) error {
		seen = event
		return nil
	})
	if err != nil || seen.Chat != "15551234567@s.whatsapp.net" || seen.Message == nil || seen.Message.ID != "m-1" {
		t.Fatalf("event = %+v, %v", seen, err)
	}
}
