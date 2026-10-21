// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/thisisjun786/lina/kit/proc"
)

func findLauncher() (string, error) {
	// PATH discovery also makes missing-launcher probes deterministic in tests.
	path, err := exec.LookPath("sandbox-exec")
	if err != nil || path != "/usr/bin/sandbox-exec" {
		return "", errors.New("launcher_missing")
	}
	return path, nil
}
func probeScript() string { return "exit 0" }
func prepare(launcher string, c Config, command Command) (proc.Command, func(), error) {
	git, err := gitPaths(c.WriteRoots)
	if err != nil {
		return proc.Command{}, func() {}, err
	}
	var policy strings.Builder
	policy.WriteString(`(version 1)
(deny default)
(allow process-exec process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow sysctl-read)
(allow file-read*)
(allow file-write-data (literal "/dev/null"))
(deny system-fcntl (fcntl-command 80 110))
`)
	for _, path := range c.WriteRoots {
		fmt.Fprintf(&policy, "(allow file-write* (subpath %q))\n", path)
		fmt.Fprintf(&policy, "(deny file-write-unlink (literal %q))\n", path)
	}
	for _, path := range git {
		fmt.Fprintf(&policy, "(deny file-write* (subpath %q))\n", path)
	}
	if command.Network {
		policy.WriteString("(allow network*)\n")
	}
	for _, path := range c.ReadDenied {
		fmt.Fprintf(&policy, "(deny file-read* file-write* (subpath %q))\n", path)
		fmt.Fprintf(&policy, "(deny network-bind (local unix-socket (subpath %q)))\n", path)
		fmt.Fprintf(&policy, "(deny network-outbound (remote unix-socket (subpath %q)))\n", path)
	}
	args := []string{"-p", policy.String(), command.Path}
	args = append(args, command.Args...)
	return proc.Command{Path: launcher, Args: args, Dir: c.WorkDir, Env: c.Env, Stdin: command.Stdin, Stdout: command.Stdout, Stderr: command.Stderr}, func() {}, nil
}
