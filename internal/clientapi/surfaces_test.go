// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSurfaceClientFallsBackToInstalledLegacyCore(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-surface-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	refusal, welcome := responseFixture(t, "unsupported_combination"), responseFixture(t, "welcome")
	conversation := protocol.Capability{Name: "conversation", Version: "1.0"}
	done := make(chan error, 1)
	go func() {
		for attempt := 0; attempt < 2; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			var request testRequest
			if err := json.NewDecoder(conn).Decode(&request); err != nil {
				conn.Close()
				done <- err
				return
			}
			hello := request.Params.Payload.(protocol.Hello)
			response := refusal
			if attempt == 0 {
				if request.Params.Protocol.Version != "1.1" {
					conn.Close()
					done <- fmt.Errorf("did not prefer 1.1")
					return
				}
			} else {
				if request.Params.Protocol.Version != "1.0" || !reflect.DeepEqual(hello.ProtocolVersions, []string{"1.0"}) || !reflect.DeepEqual(hello.Capabilities, []protocol.Capability{conversation}) {
					conn.Close()
					done <- fmt.Errorf("legacy decoder cannot read fallback: %+v", hello)
					return
				}
				response = welcome
				p := response.Payload.(protocol.Welcome)
				p.Capabilities = []protocol.Capability{conversation}
				response.Payload = p
			}
			response.ReplyTo = &request.Params.MessageID
			if err := json.NewEncoder(conn).Encode(testResponse{"2.0", request.ID, response}); err != nil {
				conn.Close()
				done <- err
				return
			}
			conn.Close()
		}
		done <- nil
	}()
	t.Cleanup(func() {
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	client, err := DialWithCapabilities(context.Background(), socket, "test", []protocol.Capability{conversation, protocol.SurfaceResultsCapability()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.Welcome.ProtocolVersion != "1.0" || !reflect.DeepEqual(client.Welcome.Capabilities, []protocol.Capability{conversation}) {
		t.Fatal("invalid fallback welcome")
	}
}
