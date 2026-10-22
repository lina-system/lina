// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package proc

import (
	"fmt"
	"io"
)

// SuperviseMain is only needed on Linux; other hosts supervise in process.
func SuperviseMain(_ []string, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "proc-supervise is supported only on Linux")
	return 69
}
