// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobDefinitionNamesOneMoreNestingLevel guards the recursion defence: a job
// definition carries the depth of the process that wrote it, plus one, and the
// helper refuses to run above the first level.
func TestJobDefinitionNamesOneMoreNestingLevel(t *testing.T) {
	marker := "<key>" + JobDepthVariable + "</key><string>"
	definition := string(jobPlist("com.lina.proc.test", "/bin/lina", "/tmp/request", "/tmp", false))
	if !strings.Contains(definition, marker+"1</string>") {
		t.Fatalf("job definition does not start at depth 1")
	}
	t.Setenv(JobDepthVariable, "1")
	definition = string(jobPlist("com.lina.proc.test", "/bin/lina", "/tmp/request", "/tmp", false))
	if !strings.Contains(definition, marker+"2</string>") {
		t.Fatalf("a job written inside a job is not deeper")
	}
}

// TestSameWriterOnlySharesComparableWriters proves the shared-writer lock is
// taken only when the two destinations really are one writer: an uncomparable
// writer must not panic the comparison, and different writers must not share a
// lock.
func TestSameWriterOnlySharesComparableWriters(t *testing.T) {
	var buffer bytes.Buffer
	if sameWriter(nil, nil) || sameWriter(&buffer, nil) {
		t.Fatal("a nil writer is never the other writer")
	}
	if !sameWriter(&buffer, &buffer) {
		t.Fatal("one writer supplied twice is not shared")
	}
	var other bytes.Buffer
	if sameWriter(&buffer, &other) {
		t.Fatal("different writers share a lock")
	}
	uncomparable := func() io.Writer { return sliceWriter{[]*bytes.Buffer{&buffer}} }
	if sameWriter(uncomparable(), uncomparable()) {
		t.Fatal("an uncomparable writer was treated as shared")
	}
}

// sliceWriter is an io.Writer whose dynamic type cannot be compared, like the
// writers the standard library itself uses.
type sliceWriter struct{ buffers []*bytes.Buffer }

func (w sliceWriter) Write(p []byte) (int, error) { return w.buffers[0].Write(p) }

// TestOwnerResolvesTheJobDirectory keeps the job's private directory out of the
// command's reach: an unset or relative directory is resolved against the
// owner's own, and an absolute one is left alone.
func TestOwnerResolvesTheJobDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ownerDirectory(""); err != nil || got != wd {
		t.Fatalf("unset directory = %q, %v; want %q", got, err, wd)
	}
	if got, err := ownerDirectory("relative/dir"); err != nil || got != strings.TrimSuffix(wd, "/")+"/relative/dir" {
		t.Fatalf("relative directory = %q, %v", got, err)
	}
	// A symlink followed by ".." is resolved by the kernel, not lexically here:
	// "link/.." must arrive uncleaned so it selects the symlink's parent. The
	// expectation is built by concatenation because filepath.Join would clean
	// the ".." away and make this check pass for the wrong implementation.
	want := strings.TrimSuffix(wd, string(filepath.Separator)) + string(filepath.Separator) + "link" + string(filepath.Separator) + ".."
	if got, err := ownerDirectory("link/.."); err != nil || got != want {
		t.Fatalf("relative directory with a symlink = %q, %v; want %q", got, err, want)
	}
	if got, err := ownerDirectory("/tmp"); err != nil || got != "/tmp" {
		t.Fatalf("absolute directory = %q, %v", got, err)
	}
	if got := ownerEnvironment(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil environment = %#v, want an empty list", got)
	}
	if got := ownerEnvironment([]string{"A=1"}); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("explicit environment = %#v", got)
	}
}
