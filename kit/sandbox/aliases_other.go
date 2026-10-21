// SPDX-License-Identifier: Apache-2.0
//go:build !linux

package sandbox

// The Linux readonly-root mount checks do not apply to other launchers.
func checkProtectedPaths([]string) error { return nil }
