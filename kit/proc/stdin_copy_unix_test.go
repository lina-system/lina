// SPDX-License-Identifier: Apache-2.0

//go:build unix

package proc

import (
	"net"
	"os"
	"syscall"
	"testing"
)

// TestSkipStdinCopyErrorKeepsAReadDeadline is a reader that is not a file,
// such as a connection whose read deadline has passed. That timeout is an
// input failure. The job closing its end of the input is not.
func TestSkipStdinCopyErrorKeepsAReadDeadline(t *testing.T) {
	deadline := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	if skipStdinCopyError(deadline) || skipStdinCopyError(os.ErrDeadlineExceeded) {
		t.Fatal("a read deadline was discarded")
	}
	for _, err := range []error{syscall.EPIPE, syscall.ECONNRESET, net.ErrClosed} {
		if !skipStdinCopyError(err) {
			t.Fatalf("%v was reported as an input failure", err)
		}
	}
}
