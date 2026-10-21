// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestUnterminatedHelloIsNotAFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "client.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, Identity{MainID: "main-test", Epoch: "1"}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "hello", "method": "hello", "params": testHello(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(conn).ReadBytes('\n'); err != io.EOF || len(line) != 0 {
		t.Fatalf("unterminated frame produced reply: %s (%v)", line, err)
	}
}
