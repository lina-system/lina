// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestRestartProtocolAdmission(t *testing.T) {
	for _, name := range []string{"no capability", "manifest excludes lifecycle", "controller absent", "stale epoch", "wrong sender", "wrong method", "recovering request", "recovering read", "durable request"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			if err := store.Acquire(); err != nil {
				t.Fatal(err)
			}
			epoch, err := store.MintEpoch(0, 0)
			if err != nil {
				t.Fatal(err)
			}
			r := lifecycle.NewRegistry(nil)
			x, err := lifecycle.OpenRestarter(store, epoch, r)
			if err != nil {
				t.Fatal(err)
			}
			if err := x.Ready(); err != nil {
				t.Fatal(err)
			}
			client, server := net.Pipe()
			identity := Identity{MainID: "main-test", Epoch: "1", Restart: x, Recovering: func() bool { return name == "recovering request" || name == "recovering read" }}
			if name == "manifest excludes lifecycle" {
				identity.Supported = LocalCombinations()
				identity.Supported[0].Capabilities = []protocol.Capability{}
			}
			if name == "controller absent" {
				identity.Restart = nil
			}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				defer server.Close()
				servePeer(t.Context(), server, identity, peerIdentity{})
			}()
			t.Cleanup(func() { client.Close(); server.Close(); <-finished })
			client.SetDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(client)
			call := func(method string, e protocol.Envelope) protocol.Envelope {
				t.Helper()
				if err := json.NewEncoder(client).Encode(map[string]any{"jsonrpc": "2.0", "id": "request", "method": method, "params": e}); err != nil {
					t.Fatal(err)
				}
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Fatal(err)
				}
				var answer response
				if err := json.Unmarshal(line, &answer); err != nil {
					t.Fatal(err)
				}
				return answer.Result
			}
			caps := []protocol.Capability{}
			if name != "no capability" {
				caps = append(caps, lifecycleCapability)
			}
			peer, platform := LocalCombinations()[0].Peer, LocalCombinations()[0].Platform
			e := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: protocol.KindHello, MessageID: "hello", AttemptID: "hello", Sender: protocol.Sender{Party: protocol.PartyClient, ID: "client-test"}, Epoch: "0", SentAt: "2026-01-01T00:00:00Z", EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: protocol.Hello{Party: protocol.PartyClient, ID: "client-test", ProductVersion: "0.0.0-dev", ProtocolVersions: []string{"1.0"}, Capabilities: caps, Peer: &peer, Platform: &platform}}
			if got := call("hello", e); got.Kind != protocol.KindWelcome {
				t.Fatalf("hello: %+v", got)
			}
			e.Kind, e.Epoch, e.Payload = protocol.KindRestartRequest, "1", protocol.RestartRequest{}
			method := string(e.Kind)
			want := protocol.ErrorCode("")
			switch name {
			case "no capability", "manifest excludes lifecycle", "controller absent":
				want = protocol.ErrorCapabilityNotNegotiated
			case "stale epoch":
				e.Epoch = "0"
				want = protocol.ErrorStaleEpoch
			case "wrong sender":
				e.Sender.ID = "other"
				want = protocol.ErrorNotAuthorized
			case "wrong method":
				method = "restart_get"
				want = protocol.ErrorMalformed
			case "recovering request":
				want = protocol.ErrorUnavailable
			case "recovering read":
				e.Kind = protocol.KindRestartGet
				e.Payload = protocol.RestartGet{}
				method = string(e.Kind)
			}
			got := call(method, e)
			if want != "" {
				if got.Error == nil || got.Error.Code != want || x.Status().Owed() {
					t.Fatalf("refusal: %+v; status: %+v", got, x.Status())
				}
			} else {
				status, ok := got.Payload.(protocol.RestartStatus)
				if !ok || status.Owed != (name == "durable request") {
					t.Fatalf("status: %+v", got)
				}
				if name == "durable request" {
					saved, err := store.ReadRestart()
					if err != nil || saved == nil || !saved.Owed() || status.RequestID == nil || *status.RequestID != saved.ID {
						t.Fatalf("ack preceded persistence: %+v %v", saved, err)
					}
				}
			}
		})
	}
}
