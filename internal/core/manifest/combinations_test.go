// SPDX-License-Identifier: Apache-2.0
package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadCombinations(t *testing.T) {
	row := `{"connection":"client","peer":"tui","platform":"linux/amd64","protocolVersion":"1.0","capabilities":[],"evidence":["conformance"]}`
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"listed", `{"supportedCombinations":[` + row + `],"version":"unrelated"}`, true},
		{"empty", `{"supportedCombinations":[]}`, true},
		{"missing", `{}`, false},
		{"null", `{"supportedCombinations":null}`, false},
		{"duplicate", `{"supportedCombinations":[` + row + `,` + row + `]}`, false},
		{"missing evidence", `{"supportedCombinations":[` + strings.Replace(row, `["conformance"]`, `[]`, 1) + `]}`, false},
		{"wildcard platform", `{"supportedCombinations":[` + strings.Replace(row, `linux/amd64`, `linux/*`, 1) + `]}`, false},
		{"malformed version", `{"supportedCombinations":[` + strings.Replace(row, `1.0`, `01.0`, 1) + `]}`, false},
		{"trailing", `{"supportedCombinations":[]} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := ReadCombinations(strings.NewReader(tc.input))
			if (err == nil) != tc.valid {
				t.Fatalf("rows=%+v, err=%v", rows, err)
			}
			if err == nil {
				if _, err := json.Marshal(rows); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
