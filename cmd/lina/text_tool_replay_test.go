// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/textcall"
)

func TestTextToolReplayCommand(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := textToolReplay([]string{"../../internal/core/textcall/testdata/synthetic.jsonl"}, &out, &diagnostics)
	var report textcall.Report
	if code != 0 || json.Unmarshal(out.Bytes(), &report) != nil || !report.Passed() {
		t.Fatalf("code=%d: %s %s", code, out.String(), diagnostics.String())
	}
	for _, data := range []string{"", "{}\n", "private malformed fixture\n"} {
		path := filepath.Join(t.TempDir(), "cases.jsonl")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if code := textToolReplay([]string{path}, &out, &diagnostics); code != 1 {
			t.Fatalf("invalid replay code %d", code)
		}
		if bytes.Contains(out.Bytes(), []byte("private malformed")) {
			t.Fatal("case content leaked")
		}
	}
	if code := textToolReplay(nil, &out, &diagnostics); code != 64 {
		t.Fatalf("usage code %d", code)
	}
	if code := textToolReplay([]string{"--help"}, &out, &diagnostics); code != 0 {
		t.Fatalf("help code %d", code)
	}
	if code := textToolReplay([]string{filepath.Join(t.TempDir(), "absent")}, &out, &diagnostics); code != 1 {
		t.Fatalf("missing file code %d", code)
	}
}

// Exercise the installed command boundary, including main's dispatch and the
// process exit status. The fixture is synthetic and no Core state is opened.
func TestTextToolReplayExecutable(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	path, err := filepath.Abs("../../internal/core/textcall/testdata/synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "text-tool-replay", path).Output()
	if err != nil {
		t.Fatalf("replay: %v\n%s", err, output)
	}
	var report textcall.Report
	if err := json.Unmarshal(output, &report); err != nil || !report.Passed() {
		t.Fatalf("report: %v\n%s", err, output)
	}
	invalid := filepath.Join(t.TempDir(), "invalid cases.jsonl")
	if err := os.WriteFile(invalid, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(binary, "text-tool-replay", invalid).Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("invalid replay: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, &report); err != nil || report.Rejected != 1 || report.Passed() {
		t.Fatalf("invalid report: %v\n%s", err, output)
	}
}
