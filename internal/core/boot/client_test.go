// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestBootUsesClientManifest(t *testing.T) {
	for _, listed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unlisted", true: "listed"}[listed], func(t *testing.T) {
			root := stateRoot(t)
			rows := []clientsrv.Combination{}
			if listed {
				rows = clientsrv.LocalCombinations()
			}
			data, err := json.Marshal(map[string]any{"supportedCombinations": rows})
			if err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(root, "manifest.json")
			if err := os.WriteFile(manifest, data, 0600); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(root, "run", "client.sock")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg := boot.Config{NoSync: true, StateRoot: root, Socket: socket, ClientManifest: manifest, Client: clientsrv.Identity{MainID: "untrusted-main", Epoch: "900", PersonID: "owner-test"}}
			cfg.AfterStep = func(step boot.Step, _ boot.Snapshot) error {
				if step != boot.Ready {
					return nil
				}
				defer cancel()
				conn, err := net.Dial("unix", socket)
				if err != nil {
					return err
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				req := map[string]any{"jsonrpc": "2.0", "id": "hello", "method": "hello", "params": helloEnvelope(t, []string{"1.0"})}
				if err := json.NewEncoder(conn).Encode(req); err != nil {
					return err
				}
				var answer struct {
					Result protocol.Envelope `json:"result"`
				}
				if err := json.NewDecoder(conn).Decode(&answer); err != nil {
					return err
				}
				if answer.Result.Epoch != "1" || answer.Result.Sender.ID != "main-test" {
					t.Fatalf("boot identity was overridden: %+v", answer.Result)
				}
				if listed {
					welcome, ok := answer.Result.Payload.(protocol.Welcome)
					if !ok || welcome.PersonID != "owner-test" {
						t.Fatalf("welcome: %+v", answer.Result)
					}
				} else if answer.Result.Error == nil || answer.Result.Error.Code != protocol.ErrorUnsupportedCombination {
					t.Fatalf("unlisted: %+v", answer.Result)
				}
				return nil
			}
			if err := boot.Run(ctx, cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}
