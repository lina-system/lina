// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/kit/protocol"
)

func accountShell() *shellModel {
	m := newShell(Options{Width: 80, Profile: Monochrome}, time.Now)
	m.content.Capabilities = []protocol.Capability{{Name: "accounts", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}}
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	return m
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
