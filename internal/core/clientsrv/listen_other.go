// SPDX-License-Identifier: Apache-2.0
//go:build !linux

package clientsrv

import (
	"errors"
	"net"
)

func listenPrivate(string) (*net.UnixListener, error) {
	return nil, errors.New("private socket binding requires Linux")
}
