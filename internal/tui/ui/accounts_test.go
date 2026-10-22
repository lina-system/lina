// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func accountShell() *shellModel {
	m := newShell(Options{Width: 80, Profile: Monochrome}, time.Now)
	m.content.Capabilities = []protocol.Capability{{Name: "accounts", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}}
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	return m
}

func TestAccountDiscoveryRefusalPreservesFocus(t *testing.T) {
	for _, open := range []bool{false, true} {
		m := accountShell()
		m.accounts.open = open
		m.content.Draft.Text = "unsent input"
		s := &signInSession{ctx: t.Context(), model: m, pending: map[string]protocol.Kind{"list": protocol.KindAccountRequest}}
		consumed, err := s.response(protocol.Envelope{ReplyTo: stringPointer("list"), Error: &protocol.Error{Code: protocol.ErrorUnavailable}})
		if err != nil || !consumed {
			t.Fatalf("account refusal was not handled: %v", err)
		}
		if m.accounts.open != open || m.content.Draft.Text != "unsent input" {
			t.Fatal("background account discovery changed focus or the unsent draft")
		}
		if m.accounts.code != "unavailable" || m.accounts.busy || len(s.pending) != 0 {
			t.Fatal("account refusal did not settle its pending request")
		}
		view := m.View().Content
		if !strings.Contains(view, design.Text(m.styles.options.Language, "unavailable")) || strings.Contains(view, design.Text(m.styles.options.Language, "account.loading")) {
			t.Fatalf("settled refusal is not visible (account block open=%v):\n%s", open, view)
		}
		if m.accountsKnown {
			t.Fatal("refusal was treated as a successful account listing")
		}
	}
}

func TestAccountDiscoveryStatusTransitions(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, connected := range []bool{false, true} {
			m := accountShell()
			m.styles = NewStyles(Options{Width: 120, Profile: Monochrome, Language: language})
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
			check := func(want string) {
				t.Helper()
				if view := m.View().Content; !strings.Contains(view, design.Text(language, want)) {
					t.Fatalf("missing account state %s:\n%s", want, view)
				}
			}
			check("account.loading")
			s := &signInSession{ctx: t.Context(), model: m, pending: map[string]protocol.Kind{"list": protocol.KindAccountRequest}}
			if _, err := s.response(protocol.Envelope{ReplyTo: stringPointer("list"), Error: &protocol.Error{Code: protocol.ErrorUnavailable}}); err != nil {
				t.Fatal(err)
			}
			check("unavailable")
			rows := []protocol.AccountSummary{}
			want := "account.empty"
			if connected {
				rows = append(rows, protocol.AccountSummary{AccountID: "synthetic", Label: "Synthetic", Status: "ok"})
				want = "account.ready"
			}
			s.pending["retry"] = protocol.KindAccountRequest
			if _, err := s.response(protocol.Envelope{ReplyTo: stringPointer("retry"), Payload: protocol.AccountResult{Operation: stringPointer("list"), Accounts: &rows}}); err != nil {
				t.Fatal(err)
			}
			if !m.accountsKnown || m.accounts.code != "" || m.accounts.open {
				t.Fatal("successful retry retained the refusal or changed focus")
			}
			check(want)
			m.Update(shellBegin{Generation: 2})
			check("account.loading")
		}
	}
}

func TestAccountSignInAvailableWithoutConversation(t *testing.T) {
	m := accountShell()
	m.Update(tea.PasteMsg{Content: "/signin"})
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("account-only Core offers no actionable sign-in")
	}
	if len(m.content.Blocks) != 0 {
		t.Fatal("sign-in entered conversation history")
	}
}

func TestAccountInputWithoutConversationShowsSignIn(t *testing.T) {
	m := accountShell()
	m.Update(tea.PasteMsg{Content: "retained input"})
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || !m.accounts.open || m.accounts.code != "needs_sign_in" || m.content.Draft.Text != "retained input" || len(m.content.Blocks) != 0 {
		t.Fatal("account-only input must retain its draft and offer sign-in without sending")
	}
}

func TestSignInPasteIsMaskedAndNeverBecomesDraft(t *testing.T) {
	m := accountShell()
	m.content.Draft.Text = "keep draft"
	m.accounts = accountView{open: true, stage: "sign_in_open"}
	callback := "http://localhost/auth/callback?code=SENSITIVE-CODE&state=SENSITIVE-STATE"
	m.Update(tea.PasteMsg{Content: callback})
	if strings.Contains(m.View().Content, "SENSITIVE") || m.content.Draft.Text != "keep draft" || len(m.conversation.undo) != 0 {
		t.Fatal("callback leaked into view, draft or undo")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	redirect, ok := command().(shellRedirect)
	if !ok || redirect.Request.URL == nil || *redirect.Request.URL != callback || m.accounts.input != "" {
		t.Fatal("callback was changed or retained after submit")
	}
	if _, duplicate := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, IsRepeat: true}); duplicate != nil {
		t.Fatal("repeated enter resent callback")
	}
	m.Update(shellBegin{Generation: 2})
	if m.accounts.open || m.accounts.url != "" || m.accounts.input != "" {
		t.Fatal("disconnect retained sign-in")
	}
}

func TestAccountCommandDoesNotSendConversationInput(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/signin"})
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || len(m.content.Blocks) != 0 || m.accounts.code != "capability_not_negotiated" {
		t.Fatal("unsupported sign-in was sent as model input")
	}
}

func TestBrowserOnlyAcceptsHTTPS(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "file:///tmp/secret", "data:text/html,test", "https://user:secret@example.invalid/", "https://example.invalid/\n"} {
		if browserURL(raw) {
			t.Fatal("unsafe browser URL accepted")
		}
	}
	if !browserURL("https://example.invalid/authorize?state=synthetic") {
		t.Fatal("authorization URL rejected")
	}
}

func TestSignInCancellationFencesLateResults(t *testing.T) {
	m := accountShell()
	ctx, cancel := context.WithCancel(t.Context())
	s := &signInSession{ctx: t.Context(), flowCtx: ctx, cancel: cancel, model: m, attempt: 7}
	m.accounts = accountView{open: true, stage: "sign_in_open", input: "synthetic-private-callback"}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s.handle(shellCancelSignIn{Generation: m.generation})
	if ctx.Err() == nil {
		t.Fatal("cancel did not release sign-in tasks")
	}
	s.handle(shellSignInExpired{Generation: m.generation, Attempt: 7})
	if m.accounts.open {
		t.Fatal("late timeout reopened cancelled sign-in")
	}
	m.accounts = accountView{open: true, stage: "sign_in_open"}
	s.handle(shellRedirect{Generation: m.generation, Attempt: 7})
	if m.accounts.busy {
		t.Fatal("old callback advanced a new form")
	}
}

func TestAccountAddUsesProviderPurposeAndSignOutUsesSelectedID(t *testing.T) {
	m := accountShell()
	m.accounts = accountView{open: true}
	m.accountRows = []protocol.AccountSummary{{AccountID: "first", Label: "First", Status: "ok"}, {AccountID: "second", Label: "Second", Status: "ok"}}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, command := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	request := command().(shellAccountRequest).Request
	if request.Operation == nil || *request.Operation != "sign_out" || request.AccountID == nil || *request.AccountID != "second" {
		t.Fatal("sign-out changed target")
	}
	m.accounts = accountView{open: true}
	_, command = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	request = command().(shellAccountRequest).Request
	if request.Purpose == nil || *request.Purpose != "provider" || request.AccountID != nil {
		t.Fatal("add account requested an owner login")
	}
}

func TestAccountManagementWithoutSignInRelay(t *testing.T) {
	m := accountShell()
	m.content.Capabilities = []protocol.Capability{{Name: "accounts", Version: "1.0"}}
	m.Update(tea.PasteMsg{Content: "/accounts"})
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("account management requires an unrelated relay capability")
	}
	request := command().(shellAccountRequest).Request
	if request.Operation == nil || *request.Operation != "list" {
		t.Fatal("account list was not requested")
	}
	m.accounts = accountView{open: true}
	m.accountRows = []protocol.AccountSummary{{AccountID: "existing", Label: "Existing", Status: "ok"}}
	_, command = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	if command != nil || m.accounts.code != "capability_not_negotiated" {
		t.Fatal("reauthorization ignored the missing relay capability")
	}
}
