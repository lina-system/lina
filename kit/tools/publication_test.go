// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationDoesNotEraseReplacementAfterRevisionCheck(t *testing.T) {
	for _, operation := range []string{"write", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f, root, _ := fileStore(t)
			name := filepath.Join(root, "note")
			if err := os.WriteFile(name, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
			if err != nil {
				t.Fatal(err)
			}
			f.observe = func(step string) {
				if step != "checked" {
					return
				}
				external := filepath.Join(root, "external")
				if err := os.WriteFile(external, []byte("newer edit"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(external, name); err != nil {
					t.Fatal(err)
				}
			}
			request := FileRequest{Operation: operation, Path: "note", Revision: read.Revision}
			if operation == "write" {
				request.Content = []byte("tool bytes")
			}
			result, err := f.Execute(t.Context(), request)
			if !errors.Is(err, ErrUnknown) {
				t.Fatalf("missed concurrent edit: %v", err)
			}
			assertCaptured(t, root, result, "newer edit")
			data, err := os.ReadFile(name)
			if err != nil || string(data) != "newer edit" {
				t.Fatalf("external edit erased: %q %v", data, err)
			}
		})
	}
}

func assertCaptured(t *testing.T, root string, result FileResult, want string) {
	t.Helper()
	if len(result.Changes) != 1 || result.Changes[0].Recovery == "" {
		t.Fatalf("missing recovery: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, result.Changes[0].Recovery))
	if err != nil || string(data) != want {
		t.Fatalf("captured bytes: %q %v", data, err)
	}
}

func TestPublicationDoesNotClobberDestinationRecreatedAfterCapture(t *testing.T) {
	for _, operation := range []string{"write", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f, root, _ := fileStore(t)
			name := filepath.Join(root, "note")
			if err := os.WriteFile(name, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
			if err != nil {
				t.Fatal(err)
			}
			f.observe = func(step string) {
				if step == "captured" {
					if err := os.WriteFile(name, []byte("recreated"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			req := FileRequest{Operation: operation, Path: "note", Revision: read.Revision}
			if operation == "write" {
				req.Content = []byte("tool")
			}
			result, err := f.Execute(t.Context(), req)
			if !errors.Is(err, ErrUnknown) {
				t.Fatalf("concurrent recreation: %v", err)
			}
			data, err := os.ReadFile(name)
			if err != nil || string(data) != "recreated" {
				t.Fatalf("recreated file lost: %q %v", data, err)
			}
			assertCaptured(t, root, result, "old")
		})
	}
}

func TestPublicationKeepsEditsThroughAnOpenDescriptorRecoverable(t *testing.T) {
	for _, operation := range []string{"write", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f, root, versions := fileStore(t)
			name := filepath.Join(root, "note")
			if err := os.WriteFile(name, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
			if err != nil {
				t.Fatal(err)
			}
			editor, err := os.OpenFile(name, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			f.observe = func(step string) {
				if step != "published" {
					return
				}
				if _, err := editor.WriteAt([]byte("external edit"), 0); err != nil {
					t.Fatal(err)
				}
				if err := editor.Sync(); err != nil {
					t.Fatal(err)
				}
			}
			req := FileRequest{Operation: operation, Path: "note", Revision: read.Revision}
			if operation == "write" {
				req.Content = []byte("tool")
			}
			result, err := f.Execute(t.Context(), req)
			if !errors.Is(err, ErrUnknown) {
				t.Fatalf("open descriptor edit: %v", err)
			}
			assertCaptured(t, root, result, "external edit")
			data, err := os.ReadFile(filepath.Join(versions, result.Changes[0].Version))
			if err != nil {
				t.Fatal(err)
			}
			var record savedVersion
			if err := json.Unmarshal(data, &record); err != nil || record.Recovery != result.Changes[0].Recovery {
				t.Fatalf("durable recovery locator: %+v %v", record, err)
			}
			f.Close()
			reopened, err := OpenFiles(root, versions)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			capture, err := reopened.Execute(t.Context(), FileRequest{Operation: "read", Path: result.Changes[0].Recovery})
			if err != nil || string(capture.Content) != "external edit" {
				t.Fatalf("reopened capture: %+v %v", capture, err)
			}
		})
	}
}
