// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const getTermios, setTermios = unix.TCGETS, unix.TCSETS

func slaveName(master *os.File) (string, error) {
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", err
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	return fmt.Sprintf("/dev/pts/%d", number), err
}
