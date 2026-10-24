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

// TerminalMatrix records the real terminals for the T-08 gate. The proposed
// list is always present. A proposed matrix names the issue that owns the
// owner's confirmation; only a confirmed matrix carries the confirmed list,
// whose entries record the observed product version and TERM.
type TerminalMatrix struct {
	Format        int             `json:"format"`
	Status        string          `json:"status"`
	DeferredTo    *int            `json:"deferred_to"`
	ProposedList  []TerminalEntry `json:"proposed_list"`
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
