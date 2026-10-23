// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin || windows

package proc_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// Each fork is acknowledged before teardown. In particular, the intermediate
// parent has already exited when the owner is asked to clean up an orphan.
func TestOwnerReapsEscapedDescendants(t *testing.T) {
	for _, escape := range escapeModes {
		for _, finish := range []string{"close", "cancel", "exit"} {
			t.Run(escape+"/"+finish, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				owner := proc.NewOwner()
				defer owner.Close(ctx)
				released := make(chan struct{})
				l := lease.New(func() { close(released) })
				defer l.Release()
				input, control, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				defer control.Close()
				output, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				defer writer.Close()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				runCtx, stop := context.WithCancel(ctx)
				defer stop()
				done := make(chan error, 1)
				go func() {
					done <- owner.Run(runCtx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestEscapingProcess$", "--", "root", escape}, Env: []string{"LINA_PROC_ESCAPE_TEST=1"}, Stdin: input, Stdout: writer, Stderr: os.Stderr})
					writer.Close()
				}()
				ready := make(chan []string, 1)
				reader := bufio.NewReader(output)
				go func() {
					first, _ := reader.ReadString('\n')
					second, _ := reader.ReadString('\n')
					ready <- []string{first, second}
				}()
				pid := 0
				select {
				case lines := <-ready:
					for _, line := range lines {
						_, _ = fmt.Sscanf(line, "pid=%d", &pid)
					}
					if pid == 0 || (lines[0] != "orphaned\n" && lines[1] != "orphaned\n") {
						t.Fatalf("readiness: %q", lines)
					}
				case <-ctx.Done():
					t.Fatal("escape readiness: ", ctx.Err())
				}
				defer killEscaped(pid) // Also clean up the red run.
				l.Release()
				select {
				case <-released:
					t.Fatal("lease released while descendants are still running")
				default:
				}
				switch finish {
				case "close":
					if err := owner.Close(ctx); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					stop()
				case "exit":
					control.Close()
				}
				select {
				case err := <-done:
					if finish == "exit" && err != nil {
						t.Fatal(err)
					}
					if finish != "exit" && err == nil {
						t.Fatal("terminated command succeeded")
					}
				case <-ctx.Done():
					t.Fatal("command did not join: ", ctx.Err())
				}
				if err := escapedGone(pid); err != nil {
					t.Fatalf("escaped descendant %d remains after %s: %v", pid, finish, err)
				}
				select {
				case <-released:
				default:
					t.Fatal("lease retained after the command tree was joined")
				}
				eof := make(chan error, 1)
				go func() { _, err := io.Copy(io.Discard, reader); eof <- err }()
				select {
				case err := <-eof:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("escaped descendant retained stdout")
				}
			})
		}
	}
}

func TestEscapingProcess(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	args := os.Args[len(os.Args)-2:]
	mode, escape := args[0], args[1]
	switch mode {
	case "leaf":
		// A leaf may run with a cleared environment, which is the point of the
		// Darwin identity test.
	case "root", "fork":
		if os.Getenv("LINA_PROC_ESCAPE_TEST") != "1" {
			return
		}
	default:
		return
	}
	if mode == "leaf" {
		fmt.Printf("pid=%s\n", strconv.Itoa(os.Getpid()))
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	next := "leaf"
	if mode == "root" {
		next = "fork"
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEscapingProcess$", "--", next, escape)
	cmd.Env = os.Environ() // Preserve the Darwin owner token through both forks.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	configureEscape(cmd, escape)
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	if mode == "root" {
		if err := cmd.Wait(); err != nil {
			panic(err)
		}
		fmt.Println("orphaned")
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	os.Exit(0)
}
