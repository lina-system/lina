// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"strings"
	"testing"
)

func TestProcessStatWithParentheses(t *testing.T) {
	info, err := parseProcessStat("42 (a tricky ) name) S 30 " + strings.Repeat("0 ", 17) + "12345 0")
	if err != nil || info.parent != 30 || info.start != "12345" {
		t.Fatalf("process stat: %+v, %v", info, err)
	}
	for _, data := range []string{"", "42 (short) S 30", "42 (bad) S wrong " + strings.Repeat("0 ", 18)} {
		if _, err := parseProcessStat(data); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}
