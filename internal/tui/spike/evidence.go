// SPDX-License-Identifier: Apache-2.0

// Package spike defines the evidence format shared by the TUI spike scenarios.
package spike

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Terminal identifies the observed terminal, not the machine or user running it.
// A pointer distinguishes an explicit local run from an omitted SSH field.
type Terminal struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	TERM    string `json:"term"`
	SSH     *bool  `json:"ssh"`
}

// Record contains one measured scenario. Bounds are declared before the run;
// metric keys include their units. Missing measurements are not passing evidence.
type Record struct {
	Format    int                        `json:"format"`
	Scenario  string                     `json:"scenario"`
	Terminal  Terminal                   `json:"terminal"`
	Libraries map[string]string          `json:"libraries"`
	Bounds    map[string]json.RawMessage `json:"bounds"`
	Measured  map[string]json.RawMessage `json:"measured"`
	Result    string                     `json:"result"`
}

var exactVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var scenarioID = regexp.MustCompile(`^S(?:[1-9]|10|11)$`)

func ReadRecord(input io.Reader) (Record, error) {
	var r Record
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return r, errors.New("expected exactly one evidence record")
	}
	return r, r.Validate()
}

func (r Record) Validate() error {
	if r.Format != 1 || !scenarioID.MatchString(r.Scenario) {
		return errors.New("unknown evidence format or scenario")
	}
	if strings.TrimSpace(r.Terminal.Name) == "" || strings.TrimSpace(r.Terminal.Version) == "" || strings.TrimSpace(r.Terminal.TERM) == "" || r.Terminal.SSH == nil {
		return errors.New("terminal name, version, TERM and SSH are required")
	}
	for _, library := range []string{"charm.land/bubbletea/v2", "charm.land/bubbles/v2", "charm.land/lipgloss/v2", "charm.land/glamour/v2", "github.com/charmbracelet/ultraviolet", "github.com/charmbracelet/x/vt"} {
		if !exactVersion.MatchString(r.Libraries[library]) {
			return fmt.Errorf("exact library revision required: %s", library)
		}
	}
	if r.Result != "pass" && r.Result != "fail" && r.Result != "gap" {
		return errors.New("result must be pass, fail or gap")
	}
	for _, metrics := range []map[string]json.RawMessage{r.Bounds, r.Measured} {
		if len(metrics) == 0 {
			return errors.New("declared bounds and measured values are required")
		}
		for name, value := range metrics {
			var scalar any
			if strings.TrimSpace(name) == "" || json.Unmarshal(value, &scalar) != nil {
				return errors.New("invalid metric")
			}
			switch v := scalar.(type) {
			case bool, float64:
			case string:
				if strings.TrimSpace(v) == "" {
					return errors.New("empty metric")
				}
			default:
				return errors.New("metric must be a non-null scalar")
			}
		}
	}
	return nil
}
