// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"encoding/json"
	"testing"
)

func TestFileSchemasBindOperationRevisionAndScope(t *testing.T) {
	spec, err := FileSpec("write_file", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"path":"file","content":"eA=="}`,
		`{"path":"../escape","revision":"absent"}`,
		`{"path":"file","revision":"absent","operation":"delete"}`,
		`{"path":"file","revision":"absent","network":true}`,
		`{"path":"file","revision":"absent","patch":"ignored"}`,
	} {
		if _, err := spec.Schema(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	in, err := spec.Schema(json.RawMessage(`{"path":"file","revision":"absent","content":"eA=="}`))
	if err != nil || !in.Write || !in.Undoable || in.Network || in.Target != "workspace" {
		t.Fatalf("schema: %+v %v", in, err)
	}
	if _, err := spec.Schema(in.Arguments); err != nil {
		t.Fatalf("normalized input rejected: %v", err)
	}
}

func TestPatchSchemaAcceptsCustomAndFunctionFormats(t *testing.T) {
	spec, err := FileSpec("apply_patch", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Add File: file\n+hello\n*** End Patch"
	function, _ := json.Marshal(map[string]string{"patch": patch})
	a, err := spec.Schema(json.RawMessage(patch))
	if err != nil {
		t.Fatal(err)
	}
	b, err := spec.Schema(function)
	if err != nil || string(a.Arguments) != string(b.Arguments) {
		t.Fatalf("formats differ: %s %s %v", a.Arguments, b.Arguments, err)
	}
	if _, err := spec.Schema(a.Arguments); err != nil {
		t.Fatal(err)
	}
}
