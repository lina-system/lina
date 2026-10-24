// SPDX-License-Identifier: Apache-2.0

package spike_test

import (
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/spike"
)

const proposedMatrix = `{
  "format": 3,
  "status": "proposed",
  "deferred_to": 371,
  "proposed_list": [
    {"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false},
    {"id": "omarchy-local", "platform": "linux-omarchy", "terminal": "installed default terminal", "ssh": false},
    {"id": "macos-ssh", "platform": "macos", "terminal": "Apple Terminal", "ssh": true}
  ],
  "confirmed_list": null
}`

const confirmedEntry = `{"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false, "version": "2.14.1", "term": "xterm-256color"}`

const confirmedMatrix = `{
  "format": 3,
  "status": "confirmed",
  "deferred_to": null,
  "proposed_list": [
    {"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false},
    {"id": "omarchy-local", "platform": "linux-omarchy", "terminal": "installed default terminal", "ssh": false},
    {"id": "macos-ssh", "platform": "macos", "terminal": "Apple Terminal", "ssh": true}
  ],
  "confirmed_list": [
    ` + confirmedEntry + `,
    {"id": "omarchy-local", "platform": "linux-omarchy", "terminal": "Alacritty", "ssh": false, "version": "0.15.1", "term": "alacritty"},
    {"id": "macos-ssh", "platform": "macos", "terminal": "Apple Terminal", "ssh": true, "version": "2.14.1", "term": "xterm-256color"}
  ]
}`

func TestTerminalMatrixRequiresOwnerState(t *testing.T) {
	matrix, err := spike.ReadTerminalMatrix(strings.NewReader(proposedMatrix))
	if err != nil {
		t.Fatal(err)
	}
	if matrix.Status != "proposed" || matrix.DeferredTo == nil || *matrix.DeferredTo != 371 {
		t.Fatalf("unexpected proposed matrix state: %+v", matrix)
	}

	if _, err := spike.ReadTerminalMatrix(strings.NewReader(confirmedMatrix)); err != nil {
		t.Fatalf("confirmed matrix rejected: %v", err)
	}

	for name, document := range map[string]string{
		"unknown status":     strings.Replace(proposedMatrix, `"proposed"`, `"awaiting_owner_confirmation"`, 1),
		"old format":         strings.Replace(proposedMatrix, `"format": 3`, `"format": 2`, 1),
		"proposed with list": strings.Replace(proposedMatrix, `"confirmed_list": null`, `"confirmed_list": []`, 1),
		"no deferral":        strings.Replace(proposedMatrix, `"deferred_to": 371,`, ``, 1),
		"confirmed empty":    strings.Replace(proposedMatrix, `"status": "proposed"`, `"status": "confirmed"`, 1),
		"proposed observed":  strings.Replace(proposedMatrix, `"ssh": true`, `"ssh": true, "version": "2.14.1"`, 1),
		"no macOS":           strings.ReplaceAll(proposedMatrix, `"platform": "macos"`, `"platform": "linux-omarchy"`),
		"no Omarchy":         strings.ReplaceAll(proposedMatrix, `"platform": "linux-omarchy"`, `"platform": "macos"`),
		"no SSH":             strings.Replace(proposedMatrix, `"ssh": true`, `"ssh": false`, 1),
		"empty terminal":     strings.Replace(proposedMatrix, `"terminal": "Apple Terminal", "ssh": true`, `"terminal": " ", "ssh": true`, 1),
		"missing ssh":        strings.Replace(proposedMatrix, `"terminal": "Apple Terminal", "ssh": true`, `"terminal": "Apple Terminal"`, 1),
		"duplicate id":       strings.Replace(proposedMatrix, `"id": "macos-ssh"`, `"id": "macos-local"`, 1),
		"unknown platform":   strings.Replace(proposedMatrix, `"platform": "linux-omarchy"`, `"platform": "linux"`, 1),
		"unknown field":      strings.Replace(proposedMatrix, `"format": 3,`, `"format": 3, "confirmed_by": "owner",`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := spike.ReadTerminalMatrix(strings.NewReader(document)); err == nil {
				t.Fatal("invalid terminal matrix accepted")
			}
		})
	}
}

func TestConfirmedTerminalMatrixRequiresObservations(t *testing.T) {
	for name, entry := range map[string]string{
		"missing version": `{"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false, "term": "xterm-256color"}`,
		"missing term":    `{"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false, "version": "2.14.1"}`,
		"blank version":   `{"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false, "version": " ", "term": "xterm-256color"}`,
		"blank term":      `{"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false, "version": "2.14.1", "term": ""}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := strings.Replace(confirmedMatrix, confirmedEntry, entry, 1)
			if _, err := spike.ReadTerminalMatrix(strings.NewReader(document)); err == nil {
				t.Fatal("confirmed terminal entry without an observation accepted")
			}
		})
	}

	for name, document := range map[string]string{
		"unproposed id": strings.Replace(confirmedMatrix, `"id": "macos-ssh"`, `"id": "macos-extra"`, 1),
		"dropped entry": strings.Replace(confirmedMatrix, confirmedEntry+`,
    {"id": "omarchy-local"`, `{"id": "omarchy-local"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := spike.ReadTerminalMatrix(strings.NewReader(document)); err == nil {
				t.Fatal("confirmed list that does not confirm the proposal accepted")
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
	if matrix.Format != 3 {
		t.Fatalf("recorded matrix format = %d, want 3", matrix.Format)
	}
	if matrix.Status != "proposed" || matrix.DeferredTo == nil || *matrix.DeferredTo != 371 {
		t.Fatalf("recorded matrix must record the owner's deferral to #371: %+v", matrix)
	}
}
