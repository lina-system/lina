// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"encoding/json"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/sandbox"
	"github.com/thisisjun786/lina/kit/tools"
)

func buildLina(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "lina")
	cmd := osexec.Command("go", "build", "-mod=readonly", "-o", binary, "../../../cmd/lina")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build lina: %v\n%s", err, output)
	}
	return binary
}

func TestExecutorCannotFallbackWhenSandboxUnavailable(t *testing.T) {
	root, versions := t.TempDir(), t.TempDir()
	if err := os.Chmod(versions, 0700); err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	e, err := coreexec.New(owner, coreexec.Config{Scope: "workspace", Root: root, Versions: versions, Lina: buildLina(t)})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(&passGate{}, leaser{})
	if err := e.RegisterFiles(registry); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	result, err := registry.Dispatch(t.Context(), tools.Call{Key: "write", Name: "write_file", Args: json.RawMessage(`{"path":"file","revision":"absent","content":"eA=="}`)})
	if !errors.Is(err, sandbox.ErrUnavailable) || result.State != tools.Failed {
		t.Fatalf("unavailable: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsandboxed effect: %v", err)
	}
}

func TestExecutorRejectsVersionStorageInsideWorkRoot(t *testing.T) {
	root := t.TempDir()
	versions := filepath.Join(root, "versions")
	if err := os.Mkdir(versions, 0700); err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	if _, err := coreexec.New(owner, coreexec.Config{Scope: "workspace", Root: root, Versions: versions, Lina: "/bin/false"}); err == nil {
		t.Fatal("workspace can overwrite retained versions")
	}
}

func TestCommandCannotReadRetainedVersionsAndRecordsFailedWrites(t *testing.T) {
	requireSandbox(t)
	root, versions := t.TempDir(), t.TempDir()
	if err := os.Chmod(versions, 0700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(versions, "retained")
	if err := os.WriteFile(protected, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	e, err := coreexec.New(owner, coreexec.Config{Scope: "workspace", Root: root, Versions: versions, Lina: buildLina(t), Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(&passGate{}, leaser{})
	argv := []string{"/bin/sh", "-c", `if [ -r "$1" ]; then exit 99; fi; printf changed > note; exit 3`, "command", protected}
	if _, err := e.RegisterCommand(registry, tools.CommandPolicy{Name: "exec_command", Target: "workspace", Prefix: argv}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(tools.CommandRequest{Argv: argv})
	result, err := registry.Dispatch(t.Context(), tools.Call{Key: "command", Name: "exec_command", Args: args})
	var receipt coreexec.CommandResult
	if parseErr := json.Unmarshal(result.Body, &receipt); parseErr != nil {
		t.Fatal(parseErr)
	}
	if err == nil || result.State != tools.Unknown || result.Revision == "" || receipt.ExitCode != 3 || len(receipt.Changes) != 1 || receipt.Changes[0].Path != "note" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
}
