// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The sole network seam maps a requested tailnet bind into an isolated loopback
// socket and supplies the synthetic source IP. The running installed Core,
// LocalAPI identity checks, protocol, model/tool loop and durable run are real.
type scenarioListener struct {
	net.Listener
	remote net.Addr
}
type scenarioConn struct {
	net.Conn
	remote net.Addr
}

func (c scenarioConn) RemoteAddr() net.Addr { return c.remote }
func (l scenarioListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return scenarioConn{c, l.remote}, nil
}

func TestM3TailnetListener(t *testing.T) {
	var scenario struct {
		Address, Remote, Input, Outcome string
		Capabilities                    []protocol.Capability
	}
	data, err := os.ReadFile("m3/tailnet-listener.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var foreground atomic.Int32
	started, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Instructions string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			// Flush headers so a later cancel is observable by both ends of the stream.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			started <- struct{}{}
			<-r.Context().Done()
			cancelled <- struct{}{}
			return
		}
		n := foreground.Add(1)
		if n > 10 {
			t.Error("foreground exceeded budget")
			w.WriteHeader(500)
			return
		}
		emitPersonaCall(w, fmt.Sprintf("read-%d", n), "skill_index", map[string]any{})
	}))
	defer provider.Close()
	// Install the executable hosting boot.Run so the normal build-digest
	// preparation check verifies the actual running code, without bypasses.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	bundle := makeInstallBundle(t, h.root, executable, buildCommand(t, "lina-node"))
	h, _ = installConversationBundle(t, h, bundle, provider.URL)
	route, err := modelpath.ResponsesFromEnv(func(name string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": provider.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "tailscaled.sock")
	daemon, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	localAPI := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "local-tailscaled.sock" {
			t.Error("LocalAPI host changed")
		}
		switch r.URL.Path {
		case "/localapi/v0/status":
			fmt.Fprint(w, `{"BackendState":"Running","TailscaleIPs":["100.100.0.1"],"Self":{"ID":"self","UserID":7}}`)
		case "/localapi/v0/whois":
			if r.URL.Query().Get("addr") != scenario.Remote {
				t.Error("WhoIs did not use transport identity")
			}
			fmt.Fprint(w, `{"Node":{"StableID":"remote","User":7,"Addresses":["100.100.0.2/32"]},"UserProfile":{"ID":7}}`)
		default:
			w.WriteHeader(404)
		}
	})}
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- localAPI.Serve(daemon) }()
	defer func() { localAPI.Close(); <-daemonDone }()
	bound := make(chan net.Addr, 1)
	ready := make(chan struct{})
	recovering := make(chan struct{})
	resume := make(chan struct{})
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	cfg := boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route, Tailnet: &clientsrv.TailnetConfig{Address: scenario.Address, Socket: socket, Listen: func(ctx context.Context, network, address string) (net.Listener, error) {
		if network != "tcp" || address != scenario.Address {
			return nil, fmt.Errorf("unexpected bind %s %s", network, address)
		}
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		remote, err := net.ResolveTCPAddr("tcp", scenario.Remote)
		if err != nil {
			l.Close()
			return nil, err
		}
		bound <- l.Addr()
		return scenarioListener{l, remote}, nil
	}}, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.SocketsBound {
			close(recovering)
			select {
			case <-resume:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if step == boot.Ready {
			close(ready)
		}
		return nil
	}}
	go func() { done <- boot.Run(ctx, cfg) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(operationTimeout):
			t.Error("Core did not join listeners")
		}
	}()
	select {
	case <-recovering:
	case err := <-done:
		done <- err
		t.Fatalf("Core failed: %v", err)
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	address := <-bound
	connect := func(network, address string) *client {
		conn, err := net.DialTimeout(network, address, operationTimeout)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
		hello := clientHello([]string{"1.0"})
		hello.Capabilities = scenario.Capabilities
		answer := c.call(t, protocol.KindHello, hello)
		welcome, ok := answer.Payload.(protocol.Welcome)
		if !ok || answer.Error != nil {
			t.Fatalf("welcome: %+v", answer)
		}
		c.welcome = welcome
		return c
	}
	local, remote := connect("unix", h.socket), connect("tcp", address.String())
	if !reflect.DeepEqual(local.welcome, remote.welcome) || len(remote.welcome.Capabilities) != 2 {
		t.Fatalf("different welcomes: %+v / %+v", local.welcome, remote.welcome)
	}
	for _, c := range []*client{local, remote} {
		answer := c.call(t, protocol.KindStop, protocol.Stop{})
		if answer.Error == nil || answer.Error.Code != protocol.ErrorUnavailable {
			t.Fatalf("recovering stop: %+v", answer)
		}
	}
	close(resume)
	select {
	case <-ready:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	history := remote.call(t, protocol.KindHistoryGet, protocol.HistoryGet{})
	conversation, ok := history.Payload.(protocol.History)
	if !ok || conversation.ConversationID == nil {
		t.Fatalf("history: %+v", history)
	}
	inputID := "remote-start"
	answer := remote.call(t, protocol.KindInput, protocol.Input{ConversationID: conversation.ConversationID, InputID: &inputID, Text: &scenario.Input})
	if answer.Kind != protocol.KindItemDone || answer.Error != nil {
		t.Fatalf("input: %+v", answer)
	}
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal("real background run never started", h.ctx.Err())
	}
	if foreground.Load() != 10 {
		t.Fatalf("tool rounds=%d", foreground.Load())
	}
	observer := connect("tcp", address.String())
	topic := "running"
	snapshot := observer.call(t, protocol.KindSubscribe, protocol.Subscribe{Topic: &topic})
	rows, ok := snapshot.Payload.(protocol.Snapshot)
	if !ok || len(rows.Items) != 1 {
		t.Fatalf("running snapshot: %+v", snapshot)
	}
	runID := rows.Items[0].Key
	observer.conn.Close()
	for _, c := range []*client{remote, remote, local} {
		answer := c.call(t, protocol.KindStop, protocol.Stop{RunID: &runID})
		result, ok := answer.Payload.(protocol.StopResult)
		if !ok || answer.Kind != protocol.KindStopResult || answer.EffectState != protocol.EffectApplied || answer.Error != nil || result.RunID != runID || string(result.Outcome) != scenario.Outcome {
			t.Fatalf("stop without approval: %+v", answer)
		}
	}
	select {
	case <-cancelled:
	case <-h.ctx.Done():
		t.Fatal("stop did not cancel provider", h.ctx.Err())
	}
	// Shutdown must close an incomplete remote handshake too.
	idle, err := net.Dial("tcp", address.String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	cancel()
	idle.SetReadDeadline(time.Now().Add(operationTimeout))
	if _, err = idle.Read(make([]byte, 1)); err == nil {
		t.Fatal("incomplete remote handshake survived shutdown")
	} else if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		t.Fatalf("shutdown did not close remote connection: %v", err)
	}
}
