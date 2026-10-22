// SPDX-License-Identifier: Apache-2.0

package install

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// coreUnit escapes systemd's argument, environment and specifier expansions.
// The service runs as the user manager's user and never receives a User= setting.
func coreUnit(binary, root, socket string) (string, error) {
	args := []string{binary, "core", "--state-root", root, "--socket", socket}
	return serviceUnit("LINA Core", "", args, binary, root, socket)
}

func workerUnit(binary, root, socket string) (string, error) {
	args := []string{binary, "--canon", filepath.Join(root, "canon/canon.sqlite"), "--socket", socket}
	return serviceUnit("LINA background memory and planning", "After=lina-core.service\nRequires=lina-core.service\n", args, binary, root, socket)
}

func serviceUnit(description, dependencies string, args []string, binary, root, socket string) (string, error) {
	for _, path := range []string{binary, root, socket} {
		if !filepath.IsAbs(path) || strings.IndexFunc(path, unicode.IsControl) >= 0 {
			return "", fmt.Errorf("service paths must be absolute single-line paths")
		}
	}
	if strings.ContainsAny(binary, "\"'\\") {
		return "", fmt.Errorf("systemd cannot execute a path containing quotes or backslashes")
	}
	for i, arg := range args {
		arg = strings.ReplaceAll(arg, "%", "%%")
		// systemd does not expand environment variables in the executable token.
		if i != 0 {
			arg = strings.ReplaceAll(arg, "$", "$$")
		}
		args[i] = strconv.Quote(arg)
	}
	return "# SPDX-License-Identifier: Apache-2.0\n[Unit]\nDescription=" + description + "\n" + dependencies + "\n[Service]\nType=simple\nExecStart=" + strings.Join(args, " ") + "\nUMask=0077\nRestart=on-failure\nRestartPreventExitStatus=69 78\n\n[Install]\nWantedBy=default.target\n", nil
}

// MatchesCoreUnit checks the exact installed service paths before an updater
// controls a unit. A custom service must be updated by its owner.
func MatchesCoreUnit(contents, root, socket string) bool {
	expected, err := coreUnit(filepath.Join(root, "system/bin/lina"), root, socket)
	return err == nil && contents == expected
}
