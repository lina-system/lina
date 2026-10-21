// SPDX-License-Identifier: Apache-2.0
//go:build !linux && !darwin

package clientsrv

import (
	"errors"
	"net"
)

func peerCredentials(*net.UnixConn) (int, int, error) {
	return 0, 0, errors.New("peer credentials unavailable")
}
func inspectProcess(int) (processIdentity, error) {
	return processIdentity{}, errors.New("process ancestry unavailable")
}
