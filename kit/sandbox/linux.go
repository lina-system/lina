// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sandbox

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/thisisjun786/lina/kit/proc"
)

//go:generate go run ./internal/policygen
//go:embed policy/*.bpf
var programs embed.FS

func findLauncher() (string, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", errors.New("unsupported_architecture")
	}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return "", errors.New("launcher_missing")
	}
	return path, nil
}
func probeScript() string {
	return `while read key value rest; do case "$key" in NoNewPrivs:) nnp=$value;; Seccomp:) sec=$value;; esac; done < /proc/self/status; [ "$nnp" = 1 ] && [ "$sec" = 2 ]`
}
func prepare(launcher string, c Config, command Command) (proc.Command, func(), error) {
	empty := func() {}
	git, err := gitPaths(c.WriteRoots)
	if err != nil {
		return proc.Command{}, empty, err
	}
	mode := "blocked"
	if command.Network {
		mode = "network"
	}
	bpf, err := programs.ReadFile("policy/linux_" + runtime.GOARCH + "_" + mode + ".bpf")
	if err != nil {
		return proc.Command{}, empty, err
	}
	file, err := os.CreateTemp("", "lina-seccomp-")
	if err != nil {
		return proc.Command{}, empty, err
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	if _, err = file.Write(bpf); err != nil {
		cleanup()
		return proc.Command{}, empty, err
	}
	if _, err = file.Seek(0, 0); err != nil {
		cleanup()
		return proc.Command{}, empty, err
	}
	// Unlink before launch: the policy travels only through its descriptor.
	if err := os.Remove(file.Name()); err != nil {
		cleanup()
		return proc.Command{}, empty, err
	}
	args := []string{"--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--cap-drop", "ALL", "--ro-bind", "/", "/"}
	if !command.Network {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	// Write binds precede all masks. A mask can never be reopened by a grant.
	for _, path := range c.WriteRoots {
		args = append(args, "--bind", path, path)
	}
	for _, path := range gitAnchors(c.WriteRoots, git) {
		args = append(args, "--bind", path, path)
	}
	for _, path := range git {
		args = append(args, "--ro-bind", path, path)
	}
	for _, path := range c.ReadDenied {
		info, err := os.Stat(path)
		switch {
		case errors.Is(err, os.ErrNotExist) || err == nil && info.IsDir():
			args = append(args, "--perms", "000", "--tmpfs", path, "--remount-ro", path)
		case err != nil:
			cleanup()
			return proc.Command{}, empty, err
		default:
			args = append(args, "--ro-bind", "/dev/null", path)
		}
	}
	args = append(args, "--chdir", c.WorkDir, "--seccomp", strconv.Itoa(3), "--", command.Path)
	args = append(args, command.Args...)
	if len(bpf)%8 != 0 {
		cleanup()
		return proc.Command{}, empty, fmt.Errorf("invalid seccomp program")
	}
	return proc.Command{Path: launcher, Args: args, Dir: c.WorkDir, Env: c.Env, ExtraFiles: []*os.File{file}, Stdin: command.Stdin, Stdout: command.Stdout, Stderr: command.Stderr}, cleanup, nil
}
