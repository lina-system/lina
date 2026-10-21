// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package proc

import (
	"errors"
	"os/exec"
)

func configure(*exec.Cmd) error { return errors.New("process groups unsupported on this platform") }
func kill(*exec.Cmd) error      { return errors.New("process groups unsupported on this platform") }
