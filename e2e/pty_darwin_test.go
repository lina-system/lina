// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const getTermios, setTermios = unix.TIOCGETA, unix.TIOCSETA

func slaveName(master *os.File) (string, error) {
	fd := int(master.Fd())
	for _, request := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(fd, request, 0); err != nil {
			return "", err
		}
	}
	// Darwin's TIOCPTYGNAME writes a 128-byte NUL-terminated device path.
	var name [128]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0])))
	if errno != 0 {
		return "", errno
	}
	return unix.ByteSliceToString(name[:]), nil
}
