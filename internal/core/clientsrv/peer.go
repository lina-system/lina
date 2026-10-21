// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"errors"
	"net"
	"os"
)

// peerIdentity is obtained from the socket, never from hello or answer payloads.
// An indeterminate ancestry is not an eligible decider. The initial verdict is
// retained even if a descendant is later reparented out of Core's process tree.
type peerIdentity struct {
	pid      int
	start    string
	eligible bool
}

type processIdentity struct {
	parent int
	start  string
}

func inspectPeer(conn *net.UnixConn) (peerIdentity, error) {
	uid, pid, err := peerCredentials(conn)
	if err != nil {
		return peerIdentity{}, err
	}
	if uid != os.Geteuid() {
		return peerIdentity{}, errors.New("client peer user differs from Core")
	}
	p := peerIdentity{pid: pid}
	info, err := inspectProcess(pid)
	if err == nil {
		p.start = info.start
		p.eligible = outsideCore(pid, os.Getpid(), inspectProcess)
	}
	return p, nil
}

func (p peerIdentity) canDecide() bool {
	if !p.eligible {
		return false
	}
	current, err := inspectProcess(p.pid)
	return err == nil && current.start == p.start && outsideCore(p.pid, os.Getpid(), inspectProcess)
}

func outsideCore(pid, core int, inspect func(int) (processIdentity, error)) bool {
	seen := make(map[int]bool)
	for pid > 0 {
		if pid == core || seen[pid] {
			return false
		}
		seen[pid] = true
		info, err := inspect(pid)
		if err != nil {
			return false
		}
		if pid == 1 {
			return info.parent == 0
		}
		if info.parent <= 0 {
			return false
		}
		pid = info.parent
	}
	return false
}
