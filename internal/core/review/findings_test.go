// SPDX-License-Identifier: Apache-2.0
package review

import (
	"testing"
)

func TestFindingsRequireCompleteSchema(t *testing.T) {
	for _, raw := range []string{
		`Looks good.`,
		`{"findings":null}`,
		`{"findings":[{"id":"f1","severity":"P4","file":"src/a.go","claim":"wrong"}]}`,
		`{"findings":[{"id":"f1","severity":"P1","file":"src/a.go"}]}`,
		`{"findings":[{"id":"f1","severity":"P1","file":"../secret","claim":"wrong"}]}`,
		`{"findings":[{"id":"f1","severity":"P1","file":"src/a.go","claim":"wrong","command":"run me"}]}`,
		`{"findings":[{"id":"f1","severity":"P1","file":"src/a.go","claim":"wrong"},{"id":"f1","severity":"P2","file":"src/a.go","claim":"another"}]}`,
		`{"findings":[]} trailing prose`,
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := Parse([]byte(raw))
			if err == nil || len(got) != 0 {
				t.Fatalf("invalid reply retained findings: %+v, %v", got, err)
			}
		})
	}
}

func TestFindingsAcceptAllSeveritiesAndEmptyResult(t *testing.T) {
	for _, severity := range []string{"P0", "P1", "P2", "P3"} {
		raw := `{"findings":[{"id":"f1","severity":"` + severity + `","file":"src/a.go","claim":"Breaks the done condition."}]}`
		got, err := Parse([]byte(raw))
		if err != nil || len(got) != 1 || got[0].Severity != severity {
			t.Fatalf("valid finding: %+v, %v", got, err)
		}
	}
	if got, err := Parse([]byte(`{"findings":[]}`)); err != nil || len(got) != 0 {
		t.Fatalf("empty result: %+v, %v", got, err)
	}
}
