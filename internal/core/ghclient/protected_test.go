// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProtectedRefsReadRealCloneWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return string(out)
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base")
	git("remote", "add", "origin", "https://github.com/o/r.git")
	c, err := New(t.Context(), Config{Path: "/usr/bin/gh", Leaser: testLeaser{}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	before := git("rev-parse", "HEAD")
	refs, err := c.ProtectedRefs(t.Context(), dir, "o/r", []string{"refs/heads/dev"})
	if err != nil || refs["refs/heads/dev"]+"\n" != before {
		t.Fatalf("%v %v", refs, err)
	}
	if git("rev-parse", "HEAD") != before {
		t.Fatal("changed clone")
	}
	for _, bad := range []string{"--help", "refs/heads/../dev", "refs/tags/dev"} {
		if _, err = c.ProtectedRefs(t.Context(), dir, "o/r", []string{bad}); err == nil {
			t.Fatal(bad)
		}
	}
	if _, err = c.ProtectedRefs(t.Context(), dir, "different/repository", []string{"refs/heads/dev"}); err == nil {
		t.Fatal("wrong repository")
	}
	if _, err = c.ProtectedRefs(t.Context(), filepath.Join(dir, "missing"), "o/r", []string{"refs/heads/dev"}); err == nil {
		t.Fatal("missing clone")
	}
}
