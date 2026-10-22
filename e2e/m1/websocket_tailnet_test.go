// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/modelpath"
)

type mobileTailnetListener struct{ net.Listener }

func (l mobileTailnetListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return mobileTailnetPeer{conn}, nil
}

type mobileTailnetPeer struct{ net.Conn }

func (c mobileTailnetPeer) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("100.100.0.2"), Port: 1234}
}

// Run the production boot composition against the installed private root, with
// only Tailscale's LocalAPI and physical addresses substituted. No alternate
// Core services or client handlers are installed. The paired Unix scenario
// separately exercises the installed binary and process lifecycle.
func mobileTailnetLauncher(t *testing.T, h *harness, providerURL string, endpoint *string) func() func() {
	t.Helper()
	socket := filepath.Join(h.root, "tailscaled-test.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "local-tailscaled.sock" {
			t.Error("unexpected daemon host")
		}
		switch r.URL.Path {
		case "/localapi/v0/status":
			json.NewEncoder(w).Encode(map[string]any{"BackendState": "Running", "TailscaleIPs": []string{"100.100.0.1"}, "Self": map[string]any{"ID": "self", "UserID": 7}})
		case "/localapi/v0/whois":
			if r.URL.Query().Get("addr") != "100.100.0.2:1234" {
				t.Error("unexpected peer lookup")
			}
			json.NewEncoder(w).Encode(map[string]any{"Node": map[string]any{"StableID": "mobile", "User": 7, "Addresses": []string{"100.100.0.2/32"}}, "UserProfile": map[string]any{"ID": 7}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	apiDone := make(chan struct{})
	go func() { defer close(apiDone); api.Serve(l) }()
	t.Cleanup(func() { api.Close(); <-apiDone })
	env := map[string]string{"LINA_RESPONSES_BASE_URL": providerURL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}
	route, err := modelpath.ResponsesFromEnv(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return func() func() {
		ctx, cancel := context.WithCancel(h.ctx)
		bound := make(chan string, 1)
		done := make(chan error, 1)
		cfg := boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route, Tailnet: &clientsrv.TailnetConfig{Address: "100.100.0.1:9000", Socket: socket,
			Listen: func(ctx context.Context, network, address string) (net.Listener, error) {
				if network != "tcp" || address != "100.100.0.1:9000" {
					t.Error("boot requested another bind")
				}
				physical, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
				if err != nil {
					return nil, err
				}
				bound <- "ws://" + physical.Addr().String() + "/client"
				return mobileTailnetListener{physical}, nil
			},
		}}
		go func() { done <- boot.Run(ctx, cfg) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
		}
		t.Cleanup(stop)
		select {
		case *endpoint = <-bound:
		case err := <-done:
			done <- err
			t.Fatalf("Core failed before tailnet bind: %v", err)
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
		waitCoreReady(t, h)
		return func() {
			stop()
			for _, path := range []string{h.socket, filepath.Join(h.root, "system/core/run.sentinel")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("Core did not remove runtime file: %s (%v)", path, err)
				}
			}
		}
	}
}
