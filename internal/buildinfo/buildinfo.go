// SPDX-License-Identifier: Apache-2.0

// Package buildinfo reports the metadata embedded when a command is built.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Release builds set all three strings with go build -ldflags -X.
// Source builds keep the development version and read Go's embedded VCS revision.
var (
	Version = "0.0.1-alpha.dev"
	Commit  = ""
	Date    = ""
)

// String returns the version line without a trailing newline. Date is the UTC
// build date (YYYY-MM-DD); when set, the commit is abbreviated to seven characters.
func String() string {
	commit := Commit
	if commit == "" {
		commit = "unknown"
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" && setting.Value != "" {
					commit = setting.Value
					break
				}
			}
		}
	}
	if Date != "" {
		if len(commit) > 7 {
			commit = commit[:7]
		}
		return fmt.Sprintf("%s (commit %s, built %s)", Version, commit, Date)
	}
	return fmt.Sprintf("%s (commit %s)", Version, commit)
}
