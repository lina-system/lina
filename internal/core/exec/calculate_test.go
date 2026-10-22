// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"testing"
)

func TestCalculateExactPrivateResearchWithoutCommandExecution(t *testing.T) {
	for expression, want := range map[string]string{"1000 * (1 + 0.07)": "1070", "(0.1+0.2)/3": "1/10", "-5 + +2": "-3"} {
		got, err := coreexec.Calculate(expression)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", expression, got, err)
		}
	}
	for _, expression := range []string{"1/0", "exec(\"order\")", "1 << 3", "[1]", "1e999999999", "x+1", "0x1p999999999"} {
		if got, err := coreexec.Calculate(expression); err == nil {
			t.Fatalf("unsafe expression accepted: %s = %s", expression, got)
		}
	}
}
