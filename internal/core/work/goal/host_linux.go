// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// MainHost reads the main's actual resources through the same report shape as
// a Node. Caps/readiness come from installation authority, never a goal payload.
// It is called on startup, resource change signals and the slow safety sweep.
func MainHost(ctx context.Context, id, path string, limits Resources, workers map[string]bool) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	load, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return Node{}, err
	}
	memory, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Node{}, err
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(path, &stat); err != nil {
		return Node{}, err
	}
	if stat.Bsize <= 0 || stat.Bavail > uint64((1<<63-1)/stat.Bsize) {
		return Node{}, ErrInvalid
	}
	return hostSample(id, string(load), string(memory), int64(stat.Bavail)*stat.Bsize/(1<<20), runtime.NumCPU(), limits, workers, time.Now())
}
