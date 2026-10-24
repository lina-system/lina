// SPDX-License-Identifier: Apache-2.0
//go:build long

package workeval

import (
	"testing"
	"time"
)

// TestLong is the evaluation's long step. It is excluded from the normal
// package run and only compiled with -tags long.
func TestLong(t *testing.T) {
	time.Sleep(120 * time.Second)
}
