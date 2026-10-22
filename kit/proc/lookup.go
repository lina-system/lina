// SPDX-License-Identifier: Apache-2.0
package proc

import (
	"errors"
	"os/exec"
	"path/filepath"
)

// LookPath rejects relative executable resolution before changing workdirs.
func LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("executable must resolve to an absolute path")
	}
	return path, nil
}
