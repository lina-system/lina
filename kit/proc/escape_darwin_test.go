// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc_test

// Each macOS case starts a launchd job, so the matrix stays bounded to the
// escape the job coalition exists to close; every teardown path still runs
// against it. TestOwnerRunsCommandsAsLaunchdJobs in job_darwin_test.go
// additionally proves the orphan shares its command's job coalition.
var escapeModes = []string{"cleared"}
