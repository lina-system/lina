// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"bytes"
	"io"
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
	uncomparable := func() io.Writer { return sliceWriter{&buffer} }
	if sameWriter(uncomparable(), uncomparable()) {
		t.Fatal("an uncomparable writer was treated as shared")
	}
}

// sliceWriter is an io.Writer whose dynamic type cannot be compared.
type sliceWriter struct{ b *bytes.Buffer }

func (w sliceWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
