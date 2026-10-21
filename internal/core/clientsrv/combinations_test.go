// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSupportedCombinations(t *testing.T) {
	approval := protocol.Capability{Name: "approval", Version: "1.0"}
	conversation := protocol.Capability{Name: "conversation", Version: "1.0"}
	table := []Combination{
		{Connection: "client", Peer: "tui", Platform: "linux/amd64", ProtocolVersion: "1.9", Capabilities: []protocol.Capability{approval}, Evidence: []string{"conformance"}},
		{Connection: "client", Peer: "tui", Platform: "linux/amd64", ProtocolVersion: "1.10", Capabilities: []protocol.Capability{approval, conversation}, Evidence: []string{"conformance"}},
	}
	for _, tc := range []struct {
		name, peer, platform string
		versions             []string
		caps                 []protocol.Capability
		want                 string
		granted              []protocol.Capability
	}{
		{"highest numeric version", "tui", "linux/amd64", []string{"1.9", "1.10"}, []protocol.Capability{approval}, "1.10", []protocol.Capability{approval}},
		{"unlisted version", "tui", "linux/amd64", []string{"1.11"}, nil, "", nil},
		{"unlisted peer", "app", "linux/amd64", []string{"1.10"}, nil, "", nil},
		{"unlisted platform", "tui", "linux/arm64", []string{"1.10"}, nil, "", nil},
		{"intersection only", "tui", "linux/amd64", []string{"1.9"}, []protocol.Capability{conversation}, "1.9", []protocol.Capability{}},
		{"exact capability version", "tui", "linux/amd64", []string{"1.9"}, []protocol.Capability{{Name: "approval", Version: "2.0"}}, "1.9", []protocol.Capability{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, caps, ok := negotiate(table, tc.peer, tc.platform, tc.versions, tc.caps)
			if selected != tc.want || ok != (tc.want != "") || !reflect.DeepEqual(caps, tc.granted) {
				t.Fatalf("negotiated %q %+v %v", selected, caps, ok)
			}
		})
	}
}

func TestUnlistedPairRefusedOnWire(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		serveConn(server, Identity{MainID: "main-test", Epoch: "1", Supported: []Combination{}})
	}()
	t.Cleanup(func() { client.Close(); server.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	answer := exchange(t, client, bufio.NewReader(client), testHello(t))
	if answer.Error == nil || answer.Error.Code != protocol.ErrorUnsupportedCombination {
		t.Fatalf("unlisted combination: %+v", answer)
	}
}

func TestEpochNumber(t *testing.T) {
	for _, value := range []string{"0", "1", "18446744073709551615"} {
		if _, err := EpochNumber(value); err != nil {
			t.Fatalf("valid epoch %s: %v", value, err)
		}
	}
	for _, value := range []string{"", "00", "01", "+1", "-1", "1\n", "18446744073709551616", "1.0"} {
		if _, err := EpochNumber(value); err == nil {
			t.Fatalf("accepted invalid epoch %q", value)
		}
	}
}

func TestHelloRequiresListedPeerAndPlatform(t *testing.T) {
	row := LocalCombinations()[0]
	// The old listener guessed this transport profile instead of reading hello.
	row.Peer = "tui"
	for _, tc := range []struct {
		name           string
		peer, platform *string
		supported      bool
	}{
		{"listed", &row.Peer, &row.Platform, true},
		{"missing peer", nil, &row.Platform, false},
		{"missing platform", &row.Peer, nil, false},
		{"missing both", nil, nil, false},
		{"unlisted peer", stringPointer("unlisted-app"), &row.Platform, false},
		{"unlisted platform", &row.Peer, stringPointer("unlisted/target"), false},
		{"empty peer", stringPointer(""), &row.Platform, false},
		{"empty platform", &row.Peer, stringPointer(""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				serveConn(server, Identity{MainID: "main-test", Epoch: "1", Supported: []Combination{row}})
			}()
			t.Cleanup(func() { client.Close(); server.Close(); <-done })
			client.SetDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(client)
			req := testHello(t)
			hello := req.Payload.(protocol.Hello)
			hello.Peer, hello.Platform = tc.peer, tc.platform
			hello.ProductVersion = "tui-linux/amd64-1.0"
			req.Payload = hello
			answer := exchange(t, client, reader, req)
			if tc.supported {
				if answer.Kind != protocol.KindWelcome {
					t.Fatalf("listed hello: %+v", answer)
				}
			} else {
				if answer.Error == nil || answer.Error.Code != protocol.ErrorUnsupportedCombination {
					t.Fatalf("unlisted hello: %+v", answer)
				}
				if _, err := reader.ReadByte(); err != io.EOF {
					t.Fatalf("refused connection remained open: %v", err)
				}
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
