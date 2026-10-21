// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package sandbox

import (
	"os"
	"syscall"
)

func openMetadata(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
