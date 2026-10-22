// SPDX-License-Identifier: Apache-2.0
package jobapi

import "testing"

func TestInvalidPlanResultIsClosed(t *testing.T) {
	for _, raw := range []string{InvalidPlanResult, ` { "rejection" : "invalid_plan" } `} {
		if !IsInvalidPlanResult([]byte(raw)) {
			t.Fatalf("valid rejection refused: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"rejection":"invalid_plan","entries":[]}`,
		`{"rejection":"invalid_plan","rejection":"invalid_plan"}`,
		`{"rejection":"different"}`, `{"rejection":null}`,
		`[{"rejection":"invalid_plan"}]`, `null`, `{"rejection":`,
	} {
		if IsInvalidPlanResult([]byte(raw)) {
			t.Fatalf("malformed or extended rejection accepted: %s", raw)
		}
	}
}
