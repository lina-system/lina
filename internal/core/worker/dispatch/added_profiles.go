// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	workeracp "github.com/thisisjun786/lina/internal/core/worker/acp"
	"github.com/thisisjun786/lina/kit/proc"
)

// AddedACPFactories reads owner settings without starting a worker. Diagnostics
// are nonfatal; callers retain installed built-ins and valid sibling entries.
func AddedACPFactories(cfg Config) ([]Factory, []error) {
	const filename = "worker-profiles.json"
	refuse := func(cause string) ([]Factory, []error) {
		return nil, []error{fmt.Errorf("%s: %s", filename, cause)}
	}
	file := filepath.Join(cfg.Root, "system", "core", filename)
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return refuse("cannot read settings")
	}
	if !info.Mode().IsRegular() || info.Size() > workeracp.MaxAddedProfilesBytes {
		return refuse("expected a regular file of at most 64 KiB")
	}
	f, err := os.Open(file)
	if err != nil {
		return refuse("cannot open settings")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, workeracp.MaxAddedProfilesBytes+1))
	if err != nil {
		return refuse("cannot read settings")
	}
	var reserved []string
	for _, builtin := range workerRegistry() {
		reserved = append(reserved, builtin.Name)
	}
	profiles, problems := workeracp.ParseAddedProfiles(raw, reserved)
	for i, problem := range problems {
		problems[i] = fmt.Errorf("%s: %w", filename, problem)
	}
	var factories []Factory
	for _, profile := range profiles {
		command, err := proc.LookPath(profile.Command)
		if err != nil {
			// Names have already passed the restricted identifier grammar;
			// commands and arguments never enter startup diagnostics.
			problems = append(problems, fmt.Errorf("%s: %s: command is unavailable", filename, profile.Name))
			continue
		}
		profile.Command = command
		factories = append(factories, acpFactory(cfg, profile))
	}
	return factories, problems
}
