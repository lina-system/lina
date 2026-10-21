// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAncestryFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pid     int
		parents map[int]int
		allowed bool
	}{
		{"independent", 40, map[int]int{40: 20, 20: 1, 1: 0}, true},
		{"child", 40, map[int]int{40: 30}, false},
		{"grandchild", 40, map[int]int{40: 50, 50: 30}, false},
		{"Core itself", 30, nil, false},
		{"disappeared ancestor", 40, map[int]int{40: 20}, false},
		{"unreadable peer", 40, nil, false},
		{"cycle", 40, map[int]int{40: 20, 20: 40}, false},
		{"incomplete chain", 40, map[int]int{40: 0}, false},
		{"missing pid", 0, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := outsideCore(tc.pid, 30, func(pid int) (processIdentity, error) {
				parent, ok := tc.parents[pid]
				if !ok {
					return processIdentity{}, errors.New("unavailable")
				}
				return processIdentity{parent: parent, start: "1"}, nil
			})
			if got != tc.allowed {
				t.Fatalf("eligible=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestIndependentPeerCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "client.sock")
	ctx := t.Context()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPeerServerChild$")
	child.Env = []string{"LINA_TEST_PEER_SERVER=" + path}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	ready := make(chan error, 1)
	go func() { _, err := bufio.NewReader(stdout).ReadString('\n'); ready <- err }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer server did not start")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	var identity struct {
		PID, UID int
		Eligible bool
	}
	if err := json.NewDecoder(conn).Decode(&identity); err != nil {
		t.Fatal(err)
	}
	if identity.PID != os.Getpid() || identity.UID != os.Geteuid() || !identity.Eligible {
		t.Fatalf("independent peer: %+v", identity)
	}
}

func TestPeerServerChild(t *testing.T) {
	path := os.Getenv("LINA_TEST_PEER_SERVER")
	if path == "" {
		t.Skip("subprocess helper")
	}
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	fmt.Println("ready")
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := inspectPeer(conn)
	if err != nil {
		t.Fatal(err)
	}
	uid, pid, err := peerCredentials(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(struct {
		PID, UID int
		Eligible bool
	}{pid, uid, peer.canDecide()}); err != nil {
		t.Fatal(err)
	}
}

func TestDeciderIdentityCannotBeReused(t *testing.T) {
	current, err := inspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []peerIdentity{
		{pid: os.Getpid(), start: current.start, eligible: true},
		{pid: os.Getppid(), start: "another-process-generation", eligible: true},
		{pid: -1, start: "1", eligible: true},
		{pid: os.Getppid(), eligible: false},
	} {
		if p.canDecide() {
			t.Fatalf("accepted unsafe decider: %+v", p)
		}
	}
}
