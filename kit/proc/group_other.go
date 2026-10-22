// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin && !windows

package proc

import (
	"errors"
)

func newProcess(Command) (process, error) {
	return nil, errors.New("process supervision unsupported on this platform")
}
