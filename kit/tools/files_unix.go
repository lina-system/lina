// SPDX-License-Identifier: Apache-2.0
//go:build linux || darwin

package tools

import (
	"os"
	"syscall"
)

func openFileNoFollow(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
