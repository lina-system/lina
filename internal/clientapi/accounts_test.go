// SPDX-License-Identifier: Apache-2.0

package clientapi_test

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestAccountRequestsUseReceiveCorrelation(t *testing.T) {
	socket, _ := streamServer(t)
	client, err := clientapi.DialWithCapabilities(t.Context(), socket, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	api, ok := any(client).(interface {
		Account(context.Context, protocol.AccountRequest) (string, error)
		SignInListening(context.Context, protocol.SignInListening) (string, error)
		SignInRedirect(context.Context, protocol.SignInRedirect) (string, error)
	})
	if !ok {
		t.Fatal("client has no typed account and relay requests")
	}
	operation, provider, purpose := "sign_in", "chatgpt", "login"
	reason, callback := "remote_terminal", "http://localhost/auth/callback?code=synthetic&state=synthetic"
	for _, send := range []func() (string, error){
		func() (string, error) {
			return api.Account(t.Context(), protocol.AccountRequest{Operation: &operation, Provider: &provider, Purpose: &purpose})
		},
		func() (string, error) {
			return api.SignInListening(t.Context(), protocol.SignInListening{CannotListen: &reason})
		},
		func() (string, error) {
			return api.SignInRedirect(t.Context(), protocol.SignInRedirect{URL: &callback})
		},
	} {
		id, err := send()
		if err != nil {
			t.Fatal(err)
		}
		reply, err := client.Receive(t.Context())
		if err != nil || reply.ReplyTo == nil || *reply.ReplyTo != id {
			t.Fatalf("uncorrelated response: %v", err)
		}
	}
}
