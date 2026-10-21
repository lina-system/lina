// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package sandbox

import (
	"errors"
	"github.com/thisisjun786/lina/kit/proc"
)

func findLauncher() (string, error) { return "", errors.New("unsupported_platform") }
func probeScript() string           { return "" }
func prepare(string, Config, Command) (proc.Command, func(), error) {
	return proc.Command{}, func() {}, errors.New("unsupported_platform")
}
