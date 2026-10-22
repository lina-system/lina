// SPDX-License-Identifier: Apache-2.0
package proc

import (
	"os/exec"
	"path/filepath"
)

// LookPath resolves a tool at the process boundary. Relative PATH entries must
// never select a program in a worker-controlled current directory.
func LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
