// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
)

func TestSignInListenerRelaysExactlyOnceAndCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	relay, err := ListenSignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(relay.Port()))
	base := "http://" + address
	get := func(path string, want int) {
		t.Helper()
		response, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("status=%d body=%q", response.StatusCode, body)
		}
	}
	get("/wrong?code=ignored", http.StatusNotFound)
	callback := "/auth/callback?code=synthetic%2Bcode&state=synthetic&iss=https%3A%2F%2Fissuer.invalid&client_id=synthetic-client"
	get(callback, http.StatusOK)
	select {
	case result := <-relay.Redirects():
		if result.URL == nil || *result.URL != base+callback {
			t.Fatal("callback URL changed")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	get(callback, http.StatusConflict)
	cancel()
	relay.Close()
	conn, err := net.Dial("tcp", address)
	if err == nil {
		conn.Close()
		t.Fatal("listener survived cancellation")
	}
}

func TestSignInRegisteredPortNeverFallsBack(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	if relay, err := ListenSignInPort(t.Context(), port); err == nil {
		relay.Close()
		t.Fatal("registered port silently changed")
	}
}
