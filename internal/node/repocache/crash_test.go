// SPDX-License-Identifier: Apache-2.0
//go:build linux || darwin

package repocache

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestKilledCacheProcessPreservesTaskAndRepository(t *testing.T) {
	req := Request{Repository: Repository{Owner: "fixture", Name: "repo"}, Task: "killed-task", Attempt: "attempt-one", Base: "dev", Branch: "task/killed"}
	if os.Getenv("LINA_CACHE_KILL_HELPER") == "1" {
		c, err := Open(os.Getenv("LINA_CACHE_TEST_ROOT"), gitPath(t))
		if err != nil {
			t.Fatal(err)
		}
		c.remote = func(Repository) string { return "file://" + os.Getenv("LINA_CACHE_TEST_REMOTE") }
		c.protocols = "https:file"
		checkout, err := c.Prepare(t.Context(), testLease(t), req)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(checkout.Path, "in-flight.txt"), []byte("unreconciled work"), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "task-started")
		var b [1]byte
		if _, err := os.Stdin.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		t.Fatal("helper should have been killed")
	}
	c, firstReq, upstream := fixture(t)
	first, err := c.Prepare(t.Context(), testLease(t), firstReq)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestKilledCacheProcessPreservesTaskAndRepository$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LINA_CACHE_KILL_HELPER=1", "LINA_CACHE_TEST_ROOT=" + c.root, "LINA_CACHE_TEST_REMOTE=" + upstream}
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := bufio.NewScanner(output)
	if !ready.Scan() || ready.Text() != "task-started" {
		t.Fatal("cache helper did not reach task barrier")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed process exited successfully")
	}
	killed := c.checkout(req)
	if text, err := os.ReadFile(filepath.Join(killed.Path, "in-flight.txt")); err != nil || string(text) != "unreconciled work" {
		t.Fatalf("in-flight work lost: %q %v", text, err)
	}
	if _, err := c.Prepare(t.Context(), testLease(t), req); err == nil {
		t.Fatal("killed task was automatically retried")
	}
	firstReq.Task, firstReq.Branch = "after-kill", "task/after-kill"
	after, err := c.Prepare(t.Context(), testLease(t), firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if after.RepositoryPath != first.RepositoryPath {
		t.Fatal("killed task invalidated the cache")
	}
	gitFixture(t, first.RepositoryPath, "fsck", "--full")
}

func TestCacheLockCoordinatesInstances(t *testing.T) {
	c, req, _ := fixture(t)
	other, err := Open(c.root, gitPath(t))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := other.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prepare(t.Context(), testLease(t), req); err == nil {
		t.Fatal("concurrent cache mutation was admitted")
	}
	unlock()
	if _, err := c.Prepare(t.Context(), testLease(t), req); err != nil {
		t.Fatal(err)
	}
}
