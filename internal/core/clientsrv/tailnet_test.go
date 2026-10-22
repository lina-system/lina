// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func tailnetAPI(t *testing.T, status any) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "tailscaled.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "local-tailscaled.sock" || r.URL.Path != "/localapi/v0/status" {
			t.Error("unexpected LocalAPI request")
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(status)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	t.Cleanup(func() { server.Close(); <-done })
	return socket
}

func TestTailnetRejectsEveryOtherBindAddress(t *testing.T) {
	status := map[string]any{"BackendState": "Running", "TailscaleIPs": []string{"100.100.0.1", "fd7a:115c:a1e0::1"}, "Self": map[string]any{"ID": "self", "UserID": 7}}
	socket := tailnetAPI(t, status)
	for _, addr := range []string{":9000", "0.0.0.0:9000", "[::]:9000", "127.0.0.1:9000", "192.168.1.1:9000", "8.8.8.8:9000", "example.com:9000", "100.100.0.2:9000", "[fd7a:115c:a1e0::2]:9000", "[::ffff:100.100.0.1]:9000"} {
		t.Run(addr, func(t *testing.T) {
			called := false
			_, err := ListenTailnet(t.Context(), TailnetConfig{Address: addr, Socket: socket, Listen: func(context.Context, string, string) (net.Listener, error) {
				called = true
				return nil, errors.New("unexpected bind")
			}})
			if err == nil || called {
				t.Fatalf("unsafe address reached bind: %s, %v", addr, err)
			}
		})
	}
	for _, addr := range []string{"100.100.0.1:9000", "[fd7a:115c:a1e0::1]:9000"} {
		bound := ""
		marker := errors.New("bound exact address")
		_, err := ListenTailnet(t.Context(), TailnetConfig{Address: addr, Socket: socket, Listen: func(_ context.Context, network, address string) (net.Listener, error) {
			if network != "tcp" {
				t.Errorf("network: %s", network)
			}
			bound = address
			return nil, marker
		}})
		if bound != addr || !errors.Is(err, marker) {
			t.Fatalf("bind = %q, %v", bound, err)
		}
	}
}

func TestTailnetRequiresRunningDaemonAndOwner(t *testing.T) {
	for _, state := range []string{"Stopped", "Running"} {
		socket := tailnetAPI(t, map[string]any{"BackendState": state, "TailscaleIPs": []string{"100.100.0.1"}})
		called := false
		_, err := ListenTailnet(t.Context(), TailnetConfig{Address: "100.100.0.1:9000", Socket: socket, Listen: func(context.Context, string, string) (net.Listener, error) {
			called = true
			return nil, errors.New("bind")
		}})
		if err == nil || called {
			t.Fatalf("missing identity admitted: %s", state)
		}
	}
}

// Fake only the network addresses and daemon; authentication and protocol are
// production code. No tailnet credentials or host interface changes are needed.
type tailnetConn struct {
	net.Conn
	remote net.Addr
}

func (c tailnetConn) RemoteAddr() net.Addr { return c.remote }

func TestTailnetPeerIdentityAndDecisionRevocation(t *testing.T) {
	status := tailnetStatus{BackendState: "Running", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.100.0.1")}}
	status.Self = &struct {
		ID     string
		UserID uint64
	}{"self", 7}
	who := tailnetWho{}
	who.Node = &struct {
		StableID  string
		User      uint64
		Addresses []netip.Prefix
		Tags      []string
	}{"remote", 7, []netip.Prefix{netip.MustParsePrefix("100.100.0.2/32")}, nil}
	who.UserProfile = &struct{ ID uint64 }{7}
	var mu sync.Mutex
	failWho := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/localapi/v0/status":
			json.NewEncoder(w).Encode(status)
		case "/localapi/v0/whois":
			if r.URL.Query().Get("addr") != "100.100.0.2:1234" {
				t.Errorf("wrong peer lookup: %s", r.URL.RawQuery)
			}
			if failWho {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(who)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	}}
	defer transport.CloseIdleConnections()
	listener := &TailnetListener{api: &tailnetAPIClient{client: &http.Client{Transport: transport, Timeout: time.Second}, transport: transport}, address: netip.MustParseAddr("100.100.0.1"), self: "self", owner: 7}
	conn := tailnetConn{remote: &net.TCPAddr{IP: net.ParseIP("100.100.0.2"), Port: 1234}}
	peer, err := listener.inspect(t.Context(), conn)
	if err != nil || !peer.canDecide() {
		t.Fatalf("owner remote refused: %v", err)
	}
	for _, change := range []struct {
		name    string
		mutate  func()
		restore func()
	}{
		{"other owner", func() { who.Node.User = 8 }, func() { who.Node.User = 7 }},
		{"other profile", func() { who.UserProfile.ID = 8 }, func() { who.UserProfile.ID = 7 }},
		{"same node", func() { who.Node.StableID = "self" }, func() { who.Node.StableID = "remote" }},
		{"tagged node", func() { who.Node.Tags = []string{"tag:worker"} }, func() { who.Node.Tags = nil }},
		{"wrong IP", func() { who.Node.Addresses = nil }, func() { who.Node.Addresses = []netip.Prefix{netip.MustParsePrefix("100.100.0.2/32")} }},
		{"daemon stopped", func() { status.BackendState = "Stopped" }, func() { status.BackendState = "Running" }},
		{"identity switched", func() { status.Self.ID = "different" }, func() { status.Self.ID = "self" }},
		{"whois failed", func() { failWho = true }, func() { failWho = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			mu.Lock()
			change.mutate()
			mu.Unlock()
			if _, err := listener.inspect(t.Context(), conn); err == nil {
				t.Error("untrusted remote admitted")
			}
			if peer.canDecide() {
				t.Error("existing decision authority survived revocation")
			}
			mu.Lock()
			change.restore()
			mu.Unlock()
		})
	}
	for _, ip := range []string{"100.100.0.1", "127.0.0.1", "192.168.1.1", "8.8.8.8"} {
		if _, err := listener.inspect(t.Context(), tailnetConn{remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 1234}}); err == nil {
			t.Errorf("invalid peer %s admitted", ip)
		}
	}
}
