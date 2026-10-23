// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package proc

import (
	"fmt"
	"io"
)

// JobMain is the macOS launchd helper; other hosts supervise in process.
func JobMain(_ []string, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "proc-job is supported only on macOS")
	return 69
}
