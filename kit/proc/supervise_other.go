// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package proc

import (
	"fmt"
	"io"
)

// SuperviseMain is only needed on Linux; macOS runs each command as a launchd
// job and Windows assigns it to a Job Object.
func SuperviseMain(_ []string, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "proc-supervise is supported only on Linux")
	return 69
}
