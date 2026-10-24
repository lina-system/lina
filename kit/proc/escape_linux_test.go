// SPDX-License-Identifier: Apache-2.0

//go:build linux

package proc_test

// Linux commands are owned by the subreaper helper, so each escape mode is
// cheap; run all of them against every teardown path.
var escapeModes = []string{"setsid", "setpgid", "cleared"}
