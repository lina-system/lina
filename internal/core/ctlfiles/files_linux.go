// SPDX-License-Identifier: Apache-2.0
package ctlfiles

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const noFollow = syscall.O_NOFOLLOW
const nonBlock = syscall.O_NONBLOCK

func owned(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && (directory || stat.Nlink == 1)
}

func unlinked(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 0
}

func checkLocal(file *os.File) error {
	var stat syscall.Statfs_t
	if err := syscall.Fstatfs(int(file.Fd()), &stat); err != nil {
		return err
	}
	if !localFilesystem(int64(uint32(stat.Type))) {
		return fmt.Errorf("state root requires a supported local filesystem (type %#x)", stat.Type)
	}
	return nil
}

// An allowlist fails closed for network mounts, FUSE (which may be remote),
// pseudo filesystems and unknown drivers. Overlay supports container installs.
func localFilesystem(kind int64) bool {
	switch kind {
	case 0xef53, 0x58465342, 0x9123683e, 0x01021994, 0x794c7630, 0xf2f52010, 0x2fc12fc1, 0x3434, 0x52654973, 0x3153464a:
		return true
	}
	return false
}

func lockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrLocked
	}
	return err
}
