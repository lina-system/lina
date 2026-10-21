// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
)

func TestRestartCrashHelper(t *testing.T) {
	root := os.Getenv("LINA_RESTART_TEST_ROOT")
	if root == "" {
		return
	}
	s, epoch := restartStore(t, root)
	x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Request(t.Context(), "drain"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LINA_RESTART_TEST_COMMIT") == "yes" {
		if exit, _, err := x.Step(t.Context()); !exit || err != nil {
			t.Fatalf("commit: %v %v", exit, err)
		}
	}
	// The acknowledgment follows the file and directory fsyncs. The parent
	// kills this process before any cleanup or normal service shutdown.
	fmt.Fprintln(os.Stdout, "durable")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestRestartSurvivesSIGKILLAtDurableBoundaries(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			root := restartRoot(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRestartCrashHelper$")
			child.Env = []string{"LINA_RESTART_TEST_ROOT=" + root}
			if committed {
				child.Env = append(child.Env, "LINA_RESTART_TEST_COMMIT=yes")
			}
			output, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			input, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			scanner := bufio.NewScanner(output)
			if !scanner.Scan() || scanner.Text() != "durable" {
				t.Fatalf("child did not acknowledge durability: %s %v", scanner.Text(), scanner.Err())
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := child.Wait(); err == nil {
				t.Fatal("child exited normally")
			}
			s, epoch := restartStore(t, root)
			if epoch != 2 {
				t.Fatalf("epoch after kill: %d", epoch)
			}
			x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
			if !x.Status().Owed() {
				t.Fatal("kill lost owed request")
			}
			if err := x.Ready(); err != nil {
				t.Fatal(err)
			}
			if x.Status().Owed() == committed {
				t.Fatalf("wrong recovery disposition: %+v", x.Status())
			}
		})
	}
}
