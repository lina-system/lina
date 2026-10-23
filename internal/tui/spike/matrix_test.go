// SPDX-License-Identifier: Apache-2.0

package spike_test

import (
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/spike"
)

const proposedMatrix = `{
  "format": 2,
  "status": "proposed",
  "deferred_to": 371,
  "proposed_list": [
    {"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false},
    {"id": "omarchy-local", "platform": "linux-omarchy", "terminal": "installed default terminal", "ssh": false},
    {"id": "macos-ssh", "platform": "macos", "terminal": "Apple Terminal", "ssh": true}
  ],
  "confirmed_list": null
}`

func TestTerminalMatrixRequiresOwnerState(t *testing.T) {
	matrix, err := spike.ReadTerminalMatrix(strings.NewReader(proposedMatrix))
	if err != nil {
		t.Fatal(err)
	}
	if matrix.Status != "proposed" || matrix.DeferredTo == nil || *matrix.DeferredTo != 371 {
		t.Fatalf("unexpected proposed matrix state: %+v", matrix)
	}

	confirmed := strings.Replace(proposedMatrix, `"status": "proposed"`, `"status": "confirmed"`, 1)
	confirmed = strings.Replace(confirmed, `"confirmed_list": null`, `"confirmed_list": [`+
		`{"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false},`+
		`{"id": "omarchy-local", "platform": "linux-omarchy", "terminal": "Alacritty", "ssh": false},`+
		`{"id": "macos-ssh", "platform": "macos", "terminal": "Apple Terminal", "ssh": true}]`, 1)
	if _, err := spike.ReadTerminalMatrix(strings.NewReader(confirmed)); err != nil {
		t.Fatalf("confirmed matrix rejected: %v", err)
	}

	for name, document := range map[string]string{
		"unknown status":     strings.Replace(proposedMatrix, `"proposed"`, `"awaiting_owner_confirmation"`, 1),
		"old format":         strings.Replace(proposedMatrix, `"format": 2`, `"format": 1`, 1),
		"proposed with list": strings.Replace(proposedMatrix, `"confirmed_list": null`, `"confirmed_list": []`, 1),
		"no deferral":        strings.Replace(proposedMatrix, `"deferred_to": 371,`, ``, 1),
		"confirmed empty":    strings.Replace(proposedMatrix, `"status": "proposed"`, `"status": "confirmed"`, 1),
		"no macOS":           strings.ReplaceAll(proposedMatrix, `"platform": "macos"`, `"platform": "linux-omarchy"`),
		"no Omarchy":         strings.ReplaceAll(proposedMatrix, `"platform": "linux-omarchy"`, `"platform": "macos"`),
		"no SSH":             strings.Replace(proposedMatrix, `"ssh": true`, `"ssh": false`, 1),
		"empty terminal":     strings.Replace(proposedMatrix, `"terminal": "Apple Terminal", "ssh": true`, `"terminal": " ", "ssh": true`, 1),
		"missing ssh":        strings.Replace(proposedMatrix, `"terminal": "Apple Terminal", "ssh": true`, `"terminal": "Apple Terminal"`, 1),
		"duplicate id":       strings.Replace(proposedMatrix, `"id": "macos-ssh"`, `"id": "macos-local"`, 1),
		"unknown platform":   strings.Replace(proposedMatrix, `"platform": "linux-omarchy"`, `"platform": "linux"`, 1),
		"unknown field":      strings.Replace(proposedMatrix, `"format": 2,`, `"format": 2, "confirmed_by": "owner",`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := spike.ReadTerminalMatrix(strings.NewReader(document)); err == nil {
				t.Fatal("invalid terminal matrix accepted")
			}
		})
	}
}

func TestRecordedTerminalMatrixIsValid(t *testing.T) {
	data, err := os.ReadFile("../../../evidence/tui-spike/terminals.json")
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := spike.ReadTerminalMatrix(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if matrix.Status != "proposed" || matrix.DeferredTo == nil || *matrix.DeferredTo != 371 {
		t.Fatalf("recorded matrix must record the owner's deferral to #371: %+v", matrix)
	}
}
