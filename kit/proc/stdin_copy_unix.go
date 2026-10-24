// SPDX-License-Identifier: Apache-2.0

//go:build unix

package proc

import (
	"errors"
	"net"
	"syscall"
)

func skipStdinCopyError(err error) bool {
	// The job's end of the input closed because the command finished, which
	// is a normal end of input rather than a failed command. A read deadline
	// on the caller's reader is a failed read. copyOwnedFile drops the
	// deadline it sets to stop a file relay before this is called.
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed)
}
