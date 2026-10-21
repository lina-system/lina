// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"fmt"
	"net"
	"runtime"

	"golang.org/x/sys/unix"
)

// listenPrivate scopes the bind's umask to a private Linux filesystem context.
// Changing the process-wide mask, even under a listener mutex, could strip
// permissions from files and directories concurrently created by other services.
func listenPrivate(path string) (*net.UnixListener, error) {
	type result struct {
		listener *net.UnixListener
		err      error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		// Do not unlock: Go retires this thread when the goroutine exits, so its
		// unshared filesystem context cannot leak to another goroutine.
		if err := unix.Unshare(unix.CLONE_FS); err != nil {
			done <- result{err: fmt.Errorf("isolate socket bind umask: %w", err)}
			return
		}
		unix.Umask(0177)
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		done <- result{listener, err}
	}()
	bound := <-done
	return bound.listener, bound.err
}
