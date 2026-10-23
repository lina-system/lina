// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc_test

// Each macOS case starts a launchd job, so the matrix stays bounded: the
// cleared-environment escape is the one the job coalition exists to close, and
// every command is still torn down through all three paths. TestOwnerRunsCommandsAsLaunchdJobs
// in job_darwin_test.go additionally proves the orphan shares its command's job
// coalition.
var escapeModes = []string{"cleared"}
