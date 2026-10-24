// SPDX-License-Identifier: Apache-2.0

package spike_test

import (
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/spike"
)

const qaRuns = `[
      {"id": "foot", "terminal": "foot", "version": "1.25.0", "term": "foot", "transport": "local"},
      {"id": "ssh-tmux", "terminal": "tmux", "version": "OpenSSH 10.2p1, tmux 3.6", "term": "xterm-256color", "transport": "ssh"}
    ]`

const proposedMatrix = `{
  "format": 3,
  "status": "proposed",
  "deferred_to": 371,
  "proposed_list": [
    {"id": "macos-local", "platform": "macos", "terminal": "Apple Terminal", "ssh": false},
    {"id": "omarchy-local", "platform": "linux-omarchy", "terminal": "installed default terminal", "ssh": false},
    {"id": "macos-ssh", "platform": "macos", "terminal": "Apple Terminal", "ssh": true}
  ],
  "automated_qa": {
    "evidence": "terminal-qa/README.md",
    "owner_confirmation_deferred_to": 371,
    "runs": ` + qaRuns + `
  },
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
  ],
  "automated_qa": {
    "evidence": "terminal-qa/README.md",
    "owner_confirmation_deferred_to": 371,
    "runs": ` + qaRuns + `
  }
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
		"no qa record":       strings.Replace(proposedMatrix, `"automated_qa"`, `"automated_terminals"`, 1),
		"qa empty evidence":  strings.Replace(proposedMatrix, `"evidence": "terminal-qa/README.md"`, `"evidence": " "`, 1),
		"qa no deferral":     strings.Replace(proposedMatrix, `"owner_confirmation_deferred_to": 371,`, ``, 1),
		"qa no runs":         strings.Replace(proposedMatrix, qaRuns, `[]`, 1),
		"qa missing version": strings.Replace(proposedMatrix, `"version": "1.25.0", `, ``, 1),
		"qa product-name version": strings.Replace(proposedMatrix,
			`"version": "OpenSSH 10.2p1, tmux 3.6"`, `"version": "OpenSSH, tmux"`, 1),
		"qa missing TERM":   strings.Replace(proposedMatrix, `"term": "foot", `, ``, 1),
		"qa blank terminal": strings.Replace(proposedMatrix, `"terminal": "tmux"`, `"terminal": " "`, 1),
		"qa unknown transport": strings.Replace(proposedMatrix,
			`"transport": "local"`, `"transport": "pty"`, 1),
		"qa duplicate id": strings.Replace(proposedMatrix, `"id": "ssh-tmux"`, `"id": "foot"`, 1),
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
	qa := matrix.AutomatedQA
	if qa.Evidence != "terminal-qa/README.md" {
		t.Fatalf("recorded automated QA evidence = %q, want terminal-qa/README.md", qa.Evidence)
	}
	if qa.OwnerConfirmationDeferredTo == nil || *qa.OwnerConfirmationDeferredTo != 371 {
		t.Fatalf("recorded automated QA must defer owner confirmation to #371: %+v", qa)
	}
	transports := make(map[string]bool, 2)
	for _, run := range qa.Runs {
		transports[run.Transport] = true
	}
	if len(qa.Runs) < 2 || !transports["local"] || !transports["ssh"] {
		t.Fatalf("recorded automated QA runs must cover local and SSH terminals: %+v", qa.Runs)
	}
}
