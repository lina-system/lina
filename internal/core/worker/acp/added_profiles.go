// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const MaxAddedProfilesBytes = 64 << 10

var addedProfileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ParseAddedProfiles isolates invalid entries. It never echoes configuration
// values in diagnostics or accepts worker settings/verification overrides.
// reserved includes every built-in identity, regardless of installation.
func ParseAddedProfiles(raw []byte, reserved []string) ([]Profile, []error) {
	refuse := func(cause string) ([]Profile, []error) { return nil, []error{errors.New(cause)} }
	if len(raw) > MaxAddedProfilesBytes {
		return refuse("file exceeds 64 KiB")
	}
	var document struct {
		Version  int               `json:"version"`
		Profiles []json.RawMessage `json:"profiles"`
	}
	if !json.Valid(raw) {
		return refuse("invalid JSON")
	}
	if strictProfileJSON(raw, &document) != nil {
		return refuse("invalid fields: expected version and profiles")
	}
	if document.Version != 1 {
		return refuse("unsupported version: expected 1")
	}
	if document.Profiles == nil || len(document.Profiles) > 128 {
		return refuse("profiles must be an array of at most 128 entries")
	}
	// Count names before validating entries: two entries must never choose an
	// executable by their order, even when one has other malformed fields.
	counts := map[string]int{}
	for _, entry := range document.Profiles {
		var identity struct{ Name string }
		_ = json.Unmarshal(entry, &identity)
		counts[identity.Name]++
	}
	var profiles []Profile
	var problems []error
	for i, entry := range document.Profiles {
		p, err := parseAddedProfile(entry, reserved, counts)
		if err != nil {
			problems = append(problems, fmt.Errorf("profile %d: %w", i+1, err))
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles, problems
}

func parseAddedProfile(raw []byte, reserved []string, counts map[string]int) (Profile, error) {
	var entry *struct {
		Name    string          `json:"name"`
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	invalid := func(cause string) (Profile, error) { return Profile{}, errors.New(cause) }
	if strictProfileJSON(raw, &entry) != nil || entry == nil {
		return invalid("invalid fields: expected name, command and string args")
	}
	if !addedProfileName.MatchString(entry.Name) {
		return invalid("name must use 1–64 lowercase letters, digits or hyphens, starting with a letter")
	}
	if slices.Contains(reserved, entry.Name) {
		return invalid("name is reserved")
	}
	if counts[entry.Name] > 1 {
		return invalid("duplicate name")
	}
	if strings.TrimSpace(entry.Command) == "" {
		return invalid("command is required")
	}
	if len(entry.Command) > 4096 || strings.ContainsRune(entry.Command, 0) || strings.TrimSpace(entry.Command) != entry.Command {
		return invalid("command must be at most 4096 bytes without NUL or outer whitespace")
	}
	if strings.ContainsAny(entry.Command, `/\`) && !filepath.IsAbs(entry.Command) {
		return invalid("command must be a PATH name or an absolute path")
	}
	var args []*string
	if entry.Args != nil && (json.Unmarshal(entry.Args, &args) != nil || args == nil) {
		return invalid("invalid fields: args must be an array of strings")
	}
	if len(args) > 128 {
		return invalid("args must contain at most 128 arguments")
	}
	p := Profile{Name: entry.Name, Command: entry.Command}
	for _, arg := range args {
		if arg == nil {
			return invalid("invalid fields: args must contain strings")
		}
		if len(*arg) > 4096 || strings.ContainsRune(*arg, 0) {
			return invalid("argument must be at most 4096 bytes without NUL")
		}
		p.Args = append(p.Args, *arg)
	}
	return p, nil
}

func strictProfileJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}
