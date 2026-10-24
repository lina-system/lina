// SPDX-License-Identifier: Apache-2.0

package spike

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	matrixFormat    = 3
	platformMacOS   = "macos"
	platformOmarchy = "linux-omarchy"
)

// TerminalEntry names one terminal in the spike gate. A proposed entry carries
// only the proposal; the version and TERM are observed on the terminal and are
// recorded only in a confirmed entry.
type TerminalEntry struct {
	ID       string  `json:"id"`
	Platform string  `json:"platform"`
	Terminal string  `json:"terminal"`
	SSH      *bool   `json:"ssh"`
	Version  *string `json:"version"`
	Term     *string `json:"term"`
}

// TerminalQARun is one terminal the automated alpha QA actually drove. The
// product, version and TERM are the values observed in that terminal.
type TerminalQARun struct {
	ID        string `json:"id"`
	Terminal  string `json:"terminal"`
	Version   string `json:"version"`
	Term      string `json:"term"`
	Transport string `json:"transport"`
}

// AutomatedQA records the automated terminal QA that stands as the alpha
// evidence for the terminal list. It never replaces the owner's confirmation:
// it names the issue that still owns that confirmation.
type AutomatedQA struct {
	Evidence                    string          `json:"evidence"`
	OwnerConfirmationDeferredTo *int            `json:"owner_confirmation_deferred_to"`
	Runs                        []TerminalQARun `json:"runs"`
}

// TerminalMatrix records the real terminals for the T-08 gate. The proposed
// list is always present. A proposed matrix names the issue that owns the
// owner's confirmation; only a confirmed matrix carries the confirmed list,
// whose entries record the observed product version and TERM.
type TerminalMatrix struct {
	Format        int             `json:"format"`
	Status        string          `json:"status"`
	DeferredTo    *int            `json:"deferred_to"`
	ProposedList  []TerminalEntry `json:"proposed_list"`
	AutomatedQA   AutomatedQA     `json:"automated_qa"`
	ConfirmedList []TerminalEntry `json:"confirmed_list"`
}

func ReadTerminalMatrix(input io.Reader) (TerminalMatrix, error) {
	var matrix TerminalMatrix
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matrix); err != nil {
		return matrix, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return matrix, errors.New("expected exactly one terminal matrix record")
	}
	return matrix, matrix.Validate()
}

func (m TerminalMatrix) Validate() error {
	if m.Format != matrixFormat {
		return errors.New("unknown terminal matrix format")
	}
	if m.Status != "proposed" && m.Status != "confirmed" {
		return errors.New("terminal matrix status must be proposed or confirmed")
	}
	if len(m.ProposedList) == 0 {
		return errors.New("proposed terminal list is required")
	}
	if err := validateEntries("proposed", m.ProposedList); err != nil {
		return err
	}
	if err := m.AutomatedQA.validate(); err != nil {
		return err
	}
	switch m.Status {
	case "proposed":
		if m.ConfirmedList != nil {
			return errors.New("a proposed terminal matrix cannot carry a confirmed list")
		}
		if m.DeferredTo == nil || *m.DeferredTo <= 0 {
			return errors.New("a proposed terminal matrix must name the issue that owns confirmation")
		}
		return coversGate(m.ProposedList)
	case "confirmed":
		if len(m.ConfirmedList) == 0 {
			return errors.New("a confirmed terminal matrix requires a confirmed list")
		}
		if err := validateEntries("confirmed", m.ConfirmedList); err != nil {
			return err
		}
		if err := confirmsProposal(m.ProposedList, m.ConfirmedList); err != nil {
			return err
		}
		return coversGate(m.ConfirmedList)
	}
	return nil
}

// validate requires the automated QA record to name its evidence, to leave the
// owner's confirmation with an issue, and to describe each terminal it ran with
// the observed product, version, TERM and transport. A version counts as
// observed only when it contains a version number.
func (qa AutomatedQA) validate() error {
	if strings.TrimSpace(qa.Evidence) == "" {
		return errors.New("automated QA record needs its evidence path")
	}
	if qa.OwnerConfirmationDeferredTo == nil || *qa.OwnerConfirmationDeferredTo <= 0 {
		return errors.New("automated QA record must name the issue that owns owner confirmation")
	}
	if len(qa.Runs) == 0 {
		return errors.New("automated QA record needs at least one run")
	}
	seen := make(map[string]bool, len(qa.Runs))
	for _, run := range qa.Runs {
		if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.Terminal) == "" {
			return errors.New("automated QA run needs an id and terminal")
		}
		if strings.TrimSpace(run.Version) == "" || !hasObservedVersion(run.Version) {
			return fmt.Errorf("automated QA run needs the observed version: %s", run.ID)
		}
		if strings.TrimSpace(run.Term) == "" {
			return fmt.Errorf("automated QA run needs the observed TERM: %s", run.ID)
		}
		if run.Transport != "local" && run.Transport != "ssh" {
			return fmt.Errorf("automated QA run has an unknown transport: %s", run.ID)
		}
		if seen[run.ID] {
			return fmt.Errorf("duplicate automated QA run id: %s", run.ID)
		}
		seen[run.ID] = true
	}
	return nil
}

// hasObservedVersion reports whether version contains a version number.
// A product name alone, such as "OpenSSH, tmux", does not.
func hasObservedVersion(version string) bool {
	for _, r := range version {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// confirmsProposal requires the confirmed list to confirm the recorded proposal:
// the same terminal ids, with the observed product replacing the placeholder.
func confirmsProposal(proposed, confirmed []TerminalEntry) error {
	want := make(map[string]bool, len(proposed))
	for _, entry := range proposed {
		want[entry.ID] = true
	}
	for _, entry := range confirmed {
		if !want[entry.ID] {
			return fmt.Errorf("confirmed terminal id was not proposed: %s", entry.ID)
		}
		delete(want, entry.ID)
	}
	for id := range want {
		return fmt.Errorf("proposed terminal id is missing from the confirmed list: %s", id)
	}
	return nil
}

func validateEntries(kind string, entries []TerminalEntry) error {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Terminal) == "" || entry.SSH == nil {
			return fmt.Errorf("%s terminal entry needs an id, terminal and SSH flag", kind)
		}
		if entry.Platform != platformMacOS && entry.Platform != platformOmarchy {
			return fmt.Errorf("%s terminal entry has an unknown platform: %s", kind, entry.Platform)
		}
		switch kind {
		case "proposed":
			if entry.Version != nil || entry.Term != nil {
				return fmt.Errorf("proposed terminal entry cannot carry an observed version or TERM: %s", entry.ID)
			}
		case "confirmed":
			if entry.Version == nil || strings.TrimSpace(*entry.Version) == "" {
				return fmt.Errorf("confirmed terminal entry needs an observed version: %s", entry.ID)
			}
			if entry.Term == nil || strings.TrimSpace(*entry.Term) == "" {
				return fmt.Errorf("confirmed terminal entry needs an observed TERM: %s", entry.ID)
			}
		}
		if seen[entry.ID] {
			return fmt.Errorf("duplicate %s terminal id: %s", kind, entry.ID)
		}
		seen[entry.ID] = true
	}
	return nil
}

// coversGate applies the T-08 requirement: a macOS terminal, the Linux
// (Omarchy) desktop, and one terminal reached over SSH.
func coversGate(entries []TerminalEntry) error {
	var macos, omarchy, ssh bool
	for _, entry := range entries {
		switch entry.Platform {
		case platformMacOS:
			macos = true
		case platformOmarchy:
			omarchy = true
		}
		if entry.SSH != nil && *entry.SSH {
			ssh = true
		}
	}
	switch {
	case !macos:
		return errors.New("terminal matrix needs a macOS terminal")
	case !omarchy:
		return errors.New("terminal matrix needs the Linux (Omarchy) terminal")
	case !ssh:
		return errors.New("terminal matrix needs a terminal reached over SSH")
	}
	return nil
}
