// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package proc

import (
	"fmt"
	"io"
)

// JobDepthVariable marks a process that launchd started as a job. Hosts
// register JobMain so a job never runs the test suite by mistake; only macOS
// sets the marker.
const JobDepthVariable = "LINA_PROC_JOB_DEPTH"

// JobMain is the macOS launchd helper; other hosts supervise in process.
func JobMain(_ []string, diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "proc-job is supported only on macOS")
	return 69
}
