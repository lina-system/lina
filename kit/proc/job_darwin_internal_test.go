// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
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
