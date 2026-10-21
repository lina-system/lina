// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

type relayStub struct {
	begins, completes int
	caps              accounts.RelayCaps
	input             accounts.Relayed
	disconnected      []string
}

func (s *relayStub) Begin(_ context.Context, _ provider.ProviderID, caps accounts.RelayCaps) (accounts.Pending, error) {
	s.begins++
	s.caps = caps
	return accounts.Pending{ID: "pending", URL: "https://auth.openai.com/api/accounts/authorize", Expires: time.Now().Add(time.Minute)}, nil
}
func (s *relayStub) Complete(_ context.Context, _ string, in accounts.Relayed) (accounts.Account, error) {
	s.completes++
	s.input = in
	return accounts.Account{ID: "account", Status: accounts.OK}, nil
}
func (s *relayStub) Disconnect(id string) { s.disconnected = append(s.disconnected, id) }

func TestRelayRequiresPhasesCapabilitiesAndConnectionIdentity(t *testing.T) {
	stub := &relayStub{}
	relay := NewSignInRelay("owner", stub)
	session := Session{PersonID: "owner", ClientID: "client", ConnectionID: "connection", Capabilities: []protocol.Capability{AccountsCapability, SignInCapability}}
	call := func(session Session, p protocol.Payload) (Result, error) {
		return relay.handle(t.Context(), session, protocol.Envelope{Payload: p})
	}
	for _, payload := range []protocol.Payload{protocol.AccountRequest{}, protocol.SignInListening{}, protocol.SignInRedirect{}} {
		if _, err := call(session, payload); err == nil {
			t.Fatal("empty/out-of-phase operation accepted")
		}
	}
	operation, providerID, purpose := "sign_in", "chatgpt", "login"
	request := protocol.AccountRequest{Operation: &operation, Provider: &providerID, Purpose: &purpose}
	foreign := session
	foreign.PersonID = "other"
	if _, err := call(foreign, request); err == nil {
		t.Fatal("another person started sign-in")
	}
	unnegotiated := session
	unnegotiated.Capabilities = []protocol.Capability{AccountsCapability}
	if _, err := call(unnegotiated, request); err == nil {
		t.Fatal("relay capability not required")
	}
	if _, err := call(session, request); err != nil {
		t.Fatal(err)
	}
	port := json.Number("32123")
	sameClient := session
	sameClient.ConnectionID = "another-connection"
	if _, err := call(sameClient, protocol.SignInListening{Port: &port}); err == nil {
		t.Fatal("reused client ID stole pending attempt")
	}
	if _, err := call(session, protocol.SignInListening{Port: &port}); err != nil {
		t.Fatal(err)
	}
	code, state := "synthetic", "state"
	if _, err := call(session, protocol.SignInRedirect{Code: &code, State: &state}); err != nil {
		t.Fatal(err)
	}
	if stub.begins != 1 || stub.completes != 1 || stub.caps.ConnectionID != session.ConnectionID || stub.input.ConnectionID != session.ConnectionID {
		t.Fatal("trusted connection identity lost")
	}
	if _, err := call(session, protocol.SignInRedirect{Code: &code, State: &state}); err == nil {
		t.Fatal("replayed completion")
	}
	relay.disconnect(session)
	if len(relay.attempts) != 0 || stub.disconnected[len(stub.disconnected)-1] != session.ConnectionID {
		t.Fatal("disconnect retained attempt")
	}
}

func TestConnectionDisconnectCancelsInFlightOperation(t *testing.T) {
	client, server := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, canceled := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	capability := protocol.Capability{Name: "probe", Version: "1.0"}
	rows := LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = []protocol.Capability{capability}
	}
	go func() {
		defer close(done)
		defer server.Close()
		servePeer(ctx, server, Identity{MainID: "main-test", Epoch: "1", Supported: rows, Operations: map[protocol.Kind]Operation{
			protocol.KindAccountRequest: {Capability: capability, Handle: func(ctx context.Context, _ Session, _ protocol.Envelope) (Result, error) {
				close(entered)
				<-ctx.Done()
				close(canceled)
				return Result{}, ctx.Err()
			}},
		}}, peerIdentity{})
	}()
	t.Cleanup(func() { cancel(); client.Close(); server.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	hello := testHello(t)
	payload := hello.Payload.(protocol.Hello)
	payload.Capabilities = []protocol.Capability{capability}
	hello.Payload = payload
	welcome := exchange(t, client, bufio.NewReader(client), hello)
	if welcome.Kind != protocol.KindWelcome {
		t.Fatalf("welcome: %+v", welcome.Error)
	}
	req := hello
	req.Kind = protocol.KindAccountRequest
	req.Epoch = welcome.Epoch
	req.Payload = protocol.AccountRequest{}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "request", "method": "account_request", "params": req})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	<-entered
	client.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel operation")
	}
}

func TestSignInErrorMappingDoesNotLeakIssuerDiagnostics(t *testing.T) {
	for _, err := range []error{accounts.ErrOwnerMismatch, accounts.ErrSignInIdentity, accounts.ErrSignInInvalid, errors.New("credential secret diagnostic")} {
		mapped := signInError(err)
		var failure *OperationError
		if !errors.As(mapped, &failure) || (failure.Code != protocol.ErrorNotAuthorized && failure.Code != protocol.ErrorMalformed && failure.Code != protocol.ErrorUnavailable) {
			t.Fatal("untyped refusal")
		}
	}
}
