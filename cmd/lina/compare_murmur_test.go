// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompareMurmurCommandRefusesWithoutPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 64},
		{[]string{"--private-body"}, 64},
		{[]string{"--help"}, 0},
		{[]string{"--state-root", t.TempDir(), "--inbox", "private-body", "--person", "person", "--account", "account", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-08T00:00:00Z"}, 78},
		{[]string{"--state-root", t.TempDir(), "--inbox", "private-body", "--person", "person", "--account", "account", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-01T00:00:00Z"}, 64},
	} {
		var output, diagnostics bytes.Buffer
		if got := compareMurmurCommand(tc.args, &output, &diagnostics); got != tc.code {
			t.Fatalf("exit %d, want %d", got, tc.code)
		}
		if output.Len() != 0 || strings.Contains(diagnostics.String(), "private-body") {
			t.Fatal("refusal leaked content or returned partial data")
		}
	}
}
