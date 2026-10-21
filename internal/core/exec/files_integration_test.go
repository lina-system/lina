// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestSandboxedFileToolPatchAndTrash(t *testing.T) {
	requireSandbox(t)
	root, versions := t.TempDir(), t.TempDir()
	if err := os.Chmod(versions, 0700); err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	e, err := coreexec.New(owner, coreexec.Config{Scope: "workspace", Root: root, Versions: versions, Lina: buildLina(t), Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(&passGate{}, leaser{})
	if err := e.RegisterFiles(registry); err != nil {
		t.Fatal(err)
	}
	dispatch := func(key, name string, request tools.FileRequest) (tools.FileReply, error) {
		t.Helper()
		args, _ := json.Marshal(request)
		result, err := registry.Dispatch(t.Context(), tools.Call{Key: key, Name: name, Args: args})
		var reply tools.FileReply
		if jsonErr := json.Unmarshal(result.Body, &reply); jsonErr != nil {
			t.Fatalf("reply: %+v %v", result, jsonErr)
		}
		return reply, err
	}
	initial := []byte{0, 255, 1, '\n', 0, 128}
	created, err := dispatch("create", "write_file", tools.FileRequest{Path: "binary", Revision: tools.AbsentRevision, Content: initial})
	if err != nil || created.State != tools.Applied {
		t.Fatalf("create: %+v %v", created, err)
	}
	deleted, err := dispatch("delete", "delete_file", tools.FileRequest{Path: "binary", Revision: created.Result.Revision})
	if err != nil || deleted.State != tools.Applied {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(root, "binary")); !os.IsNotExist(err) {
		t.Fatal("delete did not remove file")
	}
	_, err = dispatch("restore", "restore_file", tools.FileRequest{Path: "binary", Revision: tools.AbsentRevision, Version: deleted.Result.Changes[0].Version})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "binary"))
	if err != nil || !bytes.Equal(initial, got) {
		t.Fatalf("restored bytes: %v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "note"), []byte("current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stale := "*** Begin Patch\n*** Update File: note\n@@\n-old\n+changed\n*** End Patch"
	reply, err := dispatch("stale", "apply_patch", tools.FileRequest{Patch: stale})
	if err == nil || reply.Code != "stale_patch_context" {
		t.Fatalf("stale: %+v %v", reply, err)
	}
	valid := "*** Begin Patch\n*** Update File: note\n@@\n-current\n+changed\n*** End Patch"
	reply, err = dispatch("patch", "apply_patch", tools.FileRequest{Patch: valid})
	if err != nil || reply.State != tools.Applied {
		t.Fatalf("patch: %+v %v", reply, err)
	}
	got, err = os.ReadFile(filepath.Join(root, "note"))
	if err != nil || string(got) != "changed\n" {
		t.Fatalf("patch bytes: %q %v", got, err)
	}
	partial := "*** Begin Patch\n*** Add File: x\n+first\n*** Add File: x/y\n+second\n*** End Patch"
	reply, err = dispatch("partial", "apply_patch", tools.FileRequest{Patch: partial})
	if err == nil || reply.State != tools.Unknown || len(reply.Result.Changes) == 0 {
		t.Fatalf("partial patch lost recovery receipts: %+v %v", reply, err)
	}
	var applied *tools.FileReceipt
	for i := range reply.Result.Changes {
		if reply.Result.Changes[i].Path == "x" {
			applied = &reply.Result.Changes[i]
		}
	}
	if applied == nil || applied.Version == "" {
		t.Fatal("missing saved version for applied file")
	}
	_, err = dispatch("undo-partial", "restore_file", tools.FileRequest{Path: "x", Revision: applied.After, Version: applied.Version})
	if err != nil {
		t.Fatalf("restore partial patch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !os.IsNotExist(err) {
		t.Fatalf("partial write not undone: %v", err)
	}
	l := lease.New(func() {})
	defer l.Release()
	snapshot, err := e.Snapshot(t.Context(), l)
	if err != nil || len(snapshot.Manifest) != 2 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
}
