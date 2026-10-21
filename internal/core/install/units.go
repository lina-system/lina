// SPDX-License-Identifier: Apache-2.0

package install

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// coreUnit escapes systemd's argument, environment and specifier expansions.
// The service runs as the user manager's user and never receives a User= setting.
func coreUnit(binary, root, socket string) (string, error) {
	args := []string{binary, "core", "--state-root", root, "--socket", socket}
	return serviceUnit("LINA Core", "", args, binary, root, socket)
}

func workerUnit(binary, root, socket string) (string, error) {
	args := []string{binary, "--state-root", root, "--socket", socket}
	return serviceUnit("LINA background memory and planning", "After=lina-core.service\nRequires=lina-core.service\n", args, binary, root, socket)
}

func serviceUnit(description, dependencies string, args []string, binary, root, socket string) (string, error) {
	for _, path := range []string{binary, root, socket} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
			return "", fmt.Errorf("service paths must be absolute single-line paths")
		}
	}
	for i, arg := range args {
		args[i] = strconv.Quote(strings.NewReplacer("%", "%%", "$", "$$").Replace(arg))
	}
	return "# SPDX-License-Identifier: Apache-2.0\n[Unit]\nDescription=" + description + "\n" + dependencies + "\n[Service]\nType=simple\nExecStart=" + strings.Join(args, " ") + "\nUMask=0077\nRestart=on-failure\nRestartPreventExitStatus=69 78\n\n[Install]\nWantedBy=default.target\n", nil
}
