// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileWorkerWireBoundsInputAndReportsRevisionFailures(t *testing.T) {
	_, root, trash := fileStore(t)
	if err := os.WriteFile(filepath.Join(root, "note"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"operation":"write","path":"note","revision":"stale","content":"eA=="}`,
		`{"operation":"delete","path":"note","revision":"stale"} {}`,
		`{"operation":"read","path":"note","root":"elsewhere"}`,
		strings.Repeat(" ", 3*MaxFileBytes+1),
	} {
		var out bytes.Buffer
		if err := ServeFiles(t.Context(), root, trash, strings.NewReader(raw), &out); err != nil {
			t.Fatal(err)
		}
		var reply FileReply
		if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.State != Failed || reply.Code == "" {
			t.Fatalf("reply: %+v %v", reply, err)
		}
	}
	var out bytes.Buffer
	if err := ServeFiles(t.Context(), root, trash, strings.NewReader(`{"operation":"read","path":"note"}`), &out); err != nil {
		t.Fatal(err)
	}
	var reply FileReply
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.State != Applied || string(reply.Result.Content) != "keep" || reply.Result.Revision == "" {
		t.Fatalf("read reply: %+v %v", reply, err)
	}
}
