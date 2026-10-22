// SPDX-License-Identifier: Apache-2.0

// Package manifest reads the provenance written beside LINA's binaries.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work/pin"
	responseconfig "github.com/thisisjun786/lina/kit/provider/responses/config"
)

// Manifest identifies a clean source commit and the binaries produced from it.
// Additional JSON fields are allowed as later components add their metadata.
type Manifest struct {
	// CRWPin is absent only in artifacts built before the work-ledger start gate.
	CRWPin                *pin.Record                      `json:"crwPin,omitempty"`
	SupportedCombinations []Combination                    `json:"supportedCombinations"`
	Version               string                           `json:"version"`
	Commit                string                           `json:"commit"`
	Date                  string                           `json:"date"`
	Target                string                           `json:"target"`
	GoToolchain           string                           `json:"goToolchain"`
	KitVersion            int64                            `json:"kitVersion"`
	Binaries              []Binary                         `json:"binaries"`
	SQLite                SQLite                           `json:"sqlite"`
	Providers             map[string]responseconfig.Config `json:"providers"`
}

// Binary describes one artifact next to manifest.json. SHA256 is lowercase hex.
type Binary struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var (
	commitPattern    = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
	digestPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	toolchainPattern = regexp.MustCompile(`^go1\.[0-9]+\.[0-9]+$`)
	versionPattern   = regexp.MustCompile(`^0\.0\.1-alpha\.([0-9]{8})\.[1-9][0-9]*$`)
	namePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Read decodes and validates the manifest at path. It does not open the binaries;
// consumers can compare their bytes with the recorded sizes and digests.
func Read(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate manifest: %w", err)
	}
	return manifest, nil
}

// Validate checks the required metadata and artifact records.
func (m Manifest) Validate() error {
	if m.CRWPin != nil {
		if err := m.CRWPin.Validate(); err != nil {
			return fmt.Errorf("crwPin: %w", err)
		}
	}
	if m.SupportedCombinations == nil {
		return fmt.Errorf("supportedCombinations is required")
	}
	if err := ValidateCombinations(m.SupportedCombinations); err != nil {
		return err
	}

	switch m.Target {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64":
	default:
		return fmt.Errorf("target must name a supported build OS/architecture")
	}
	chatgpt, ok := m.Providers["chatgpt"]
	if !ok {
		return fmt.Errorf("providers must include chatgpt")
	}
	if err := chatgpt.Validate(); err != nil {
		return fmt.Errorf("chatgpt: %w", err)
	}
	if err := m.SQLite.Validate(); err != nil {
		return fmt.Errorf("SQLite: %w", err)
	}
	if m.Version != "0.0.1-alpha.dev" {
		match := versionPattern.FindStringSubmatch(m.Version)
		if match == nil {
			return fmt.Errorf("invalid version %q", m.Version)
		}
		if _, err := time.Parse("20060102", match[1]); err != nil {
			return fmt.Errorf("invalid version date: %w", err)
		}
	}
	if !commitPattern.MatchString(m.Commit) {
		return fmt.Errorf("commit must be a full lowercase Git hash")
	}
	if _, err := time.Parse(time.DateOnly, m.Date); err != nil {
		return fmt.Errorf("invalid UTC date: %w", err)
	}
	if !toolchainPattern.MatchString(m.GoToolchain) {
		return fmt.Errorf("goToolchain must name an exact Go release")
	}
	// The writer uses JSON numbers through JavaScript; preserve exact integers.
	if m.KitVersion <= 0 || m.KitVersion > 1<<53-1 {
		return fmt.Errorf("kitVersion must be a positive safe integer")
	}
	seen := make(map[string]bool)
	for _, binary := range m.Binaries {
		if !namePattern.MatchString(binary.Name) || strings.HasSuffix(binary.Name, ".") {
			return fmt.Errorf("invalid binary name %q", binary.Name)
		}
		if seen[binary.Name] {
			return fmt.Errorf("duplicate binary %q", binary.Name)
		}
		seen[binary.Name] = true
		if binary.Size <= 0 || binary.Size > 1<<53-1 {
			return fmt.Errorf("binary %q size must be a positive safe integer", binary.Name)
		}
		if !digestPattern.MatchString(binary.SHA256) {
			return fmt.Errorf("binary %q sha256 must be 64 lowercase hex characters", binary.Name)
		}
	}
	if (seen["lina"] || seen["lina-node"]) && (seen["lina.exe"] || seen["lina-node.exe"]) {
		return fmt.Errorf("LINA binaries must use matching platform suffixes")
	}
	if !(seen["lina"] && seen["lina-node"] || seen["lina.exe"] && seen["lina-node.exe"]) {
		return fmt.Errorf("binaries must include lina and lina-node for the same platform")
	}
	if strings.HasPrefix(m.Target, "windows/") != seen["lina.exe"] {
		return fmt.Errorf("binary suffixes do not match target")
	}
	return nil
}
