// SPDX-License-Identifier: Apache-2.0

package proc_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func TestSupervisorCleansUpWhenOwnerDies(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.CommandContext(ctx, exe, "-test.run=^TestDyingOwnerProcess$")
	owner.Env = []string{"LINA_PROC_OWNER_DEATH_TEST=1"}
	output, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer owner.Process.Kill()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	var pid int
	select {
	case line := <-ready:
		if _, err := fmt.Sscanf(line, "pid=%d", &pid); err != nil {
			t.Fatal(line, err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer killEscaped(pid)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	if err := escapedGone(pid); err != nil {
		t.Fatal("owner death left descendant:", err)
	}
}

func TestDyingOwnerProcess(t *testing.T) {
	if os.Getenv("LINA_PROC_OWNER_DEATH_TEST") != "1" {
		return
	}
	owner := proc.NewOwner()
	l := lease.New(func() {})
	err := owner.Run(context.Background(), l, proc.Command{Path: os.Args[0], Args: []string{"-test.run=^TestEscapingProcess$", "--", "leaf", "setsid"}, Env: []string{}, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
}
