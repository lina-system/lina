// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// ApplicationCase exercises a service with fresh state. The adapter establishes
// Setup, executes Input, and reads Outcome from the service's recorded state.
// Unlike receive-boundary cases, Output is a service reply, not decoded input.
type ApplicationCase struct {
	Name    string          `json:"-"`
	Comment string          `json:"$comment,omitempty"`
	Service string          `json:"service"`
	Setup   json.RawMessage `json:"setup"`
	Input   json.RawMessage `json:"input"`
	Output  json.RawMessage `json:"output"`
	Outcome json.RawMessage `json:"outcome"`
}

// LoadApplications reads the application/ subtree separately from the wire
// corpus. Native service fixtures do not claim compatibility with a wire schema.
func LoadApplications(fsys fs.FS) ([]ApplicationCase, error) {
	var cases []ApplicationCase
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("%s: invalid JSON", name)
		}
		var c ApplicationCase
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if c.Service == "" {
			return fmt.Errorf("%s: service is required", name)
		}
		for field, raw := range map[string]json.RawMessage{"setup": c.Setup, "input": c.Input, "output": c.Output, "outcome": c.Outcome} {
			if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fmt.Errorf("%s: %s is required and non-null", name, field)
			}
		}
		c.Name = name
		cases = append(cases, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("application suite needs cases")
	}
	return cases, nil
}

// Application never receives expected outputs or outcomes. Observation is a
// separate read after execution, so a reply alone cannot prove a durable result.
type Application struct {
	Execute func(json.RawMessage) (json.RawMessage, error)
	Observe func() (json.RawMessage, error)
}

func RunApplication(c ApplicationCase, app Application) error {
	if app.Execute == nil || app.Observe == nil {
		return fmt.Errorf("%s: missing application adapter", c.Name)
	}
	output, err := app.Execute(append(json.RawMessage(nil), c.Input...))
	if err != nil {
		return fmt.Errorf("%s: execute: %w", c.Name, err)
	}
	if err := equalJSON(c.Output, output); err != nil {
		return fmt.Errorf("%s: output: %w; got %s, want %s", c.Name, err, output, c.Output)
	}
	outcome, err := app.Observe()
	if err != nil {
		return fmt.Errorf("%s: observe: %w", c.Name, err)
	}
	if err := equalJSON(c.Outcome, outcome); err != nil {
		return fmt.Errorf("%s: recorded outcome: %w; got %s, want %s", c.Name, err, outcome, c.Outcome)
	}
	return nil
}
