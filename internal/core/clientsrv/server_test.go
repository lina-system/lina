// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestUnsupportedEnvelopeVersion(t *testing.T) {
	for _, version := range []string{"2.0", "1.2", "0.9"} {
		t.Run(version, func(t *testing.T) {
			data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/hello.json")
			if err != nil {
				t.Fatal(err)
			}
			// Build the offered version as raw JSON: the 1.0 Go type cannot encode it.
			var envelope map[string]any
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["protocol"].(map[string]any)["version"] = version
			envelope["payload"].(map[string]any)["protocol_versions"] = []string{version}
			frame, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": "handshake", "method": "hello", "params": envelope,
			})
			if err != nil {
				t.Fatal(err)
			}
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				serveConn(server, Identity{MainID: "main-test", Epoch: "1"})
			}()
			t.Cleanup(func() { client.Close(); server.Close(); <-done })
			client.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := client.Write(append(frame, '\n')); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(client)
			line, err := reader.ReadBytes('\n')
			if err != nil {
				t.Fatalf("expected unsupported_combination response before EOF: %v", err)
			}
			var answer response
			if err := json.Unmarshal(line, &answer); err != nil {
				t.Fatal(err)
			}
			if answer.JSONRPC != "2.0" || string(answer.ID) != `"handshake"` || answer.Result.ReplyTo == nil || *answer.Result.ReplyTo != envelope["message_id"] {
				t.Fatalf("uncorrelated refusal: %s", line)
			}
			if answer.Result.Kind != protocol.KindError || answer.Result.EffectState != protocol.EffectRefused || answer.Result.Error == nil || answer.Result.Error.Code != protocol.ErrorUnsupportedCombination {
				t.Fatalf("unexpected refusal: %s", line)
			}
			if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF after refusal, got %v", err)
			}
		})
	}
}

func TestUnrecognizableFirstFrameCloses(t *testing.T) {
	for _, frame := range []string{
		`{`,
		`{"jsonrpc":"2.0","id":"handshake","method":"hello","params":{"protocol":{"name":"client","version":"2.0"}}}`,
	} {
		t.Run(frame, func(t *testing.T) {
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				serveConn(server, Identity{MainID: "main-test", Epoch: "1"})
			}()
			t.Cleanup(func() { client.Close(); server.Close(); <-done })
			client.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(client, frame+"\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := bufio.NewReader(client).ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF without a response, got %v", err)
			}
		})
	}
}

func TestListenPreservesExistingPaths(t *testing.T) {
	for _, kind := range []string{"file", "socket", "symlink", "public directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "core.sock")
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			}
			before, statErr := os.Lstat(path)
			listener, err := Listen(path)
			if err == nil {
				listener.Close()
				t.Fatal("accepted an unsafe or occupied socket path")
			}
			if statErr == nil {
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("existing path changed: %v", err)
				}
			} else {
				info, err := os.Stat(root)
				if err != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("changed existing directory: %v", err)
				}
			}
		})
	}
}

func TestServeCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "core.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, Identity{MainID: "main-test", Epoch: "1"}) }()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Leave a partial frame pending to exercise cancellation of a blocked reader.
	if _, err := conn.Write([]byte(`{"jsonrpc":`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not finish on cancellation")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
}

func serveConn(conn net.Conn, identity Identity) {
	servePeer(context.Background(), conn, identity, peerIdentity{})
}
