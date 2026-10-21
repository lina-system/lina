// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testEntry() entry {
	return entry{Version: 1, ID: "operation-1", Action: forget, At: 123, Person: "person-1", Targets: []Target{{Kind: Memory, ID: "memory-1", Holder: "all", Agent: "agent-1", Events: []string{"event-1"}, Conversations: []string{"conversation-1"}}}}
}

func TestRemovalJournalSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	j, err := openJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	e := testEntry()
	if err := j.append(e); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = openJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	entries, err := j.read()
	if err != nil || len(entries) != 1 || entries[0].Targets[0].Events[0] != "event-1" {
		t.Fatalf("reopen: %+v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatal("operation is not one removal-list line")
	}
	info, err := os.Stat(filepath.Join(root, "backups", "removals"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private journal: %v %v", info, err)
	}
}

func TestRemovalJournalRefusesTornOrChangedRecords(t *testing.T) {
	for _, damage := range []string{"torn", "changed", "duplicate", "unknown-field"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			j, err := openJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.append(testEntry()); err != nil {
				t.Fatal(err)
			}
			j.Close()
			path := filepath.Join(root, "backups", "removals")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "torn":
				data = data[:len(data)-2]
			case "changed":
				data = bytes.Replace(data, []byte("memory-1"), []byte("memory-2"), 1)
			case "duplicate":
				data = append(data, data...)
			case "unknown-field":
				data = bytes.Replace(data, []byte(`"version":1`), []byte(`"extra":1,"version":1`), 1)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			j, err = openJournal(root)
			if err == nil {
				j.Close()
				t.Fatal("corrupt removal list was accepted")
			}
		})
	}
}

func TestRemovalJournalRejectsEscapingPaths(t *testing.T) {
	for _, dir := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[dir], func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			var path, target string
			if dir {
				path, target = filepath.Join(root, "backups"), outside
			} else {
				if err := os.Mkdir(filepath.Join(root, "backups"), 0700); err != nil {
					t.Fatal(err)
				}
				path, target = filepath.Join(root, "backups", "removals"), filepath.Join(outside, "removals")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			j, err := openJournal(root)
			if err == nil {
				j.Close()
				t.Fatal("followed journal symlink")
			}
		})
	}
}

func TestRemovalJournalSyncFailureIsNotAcknowledged(t *testing.T) {
	j, err := openJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	failed := errors.New("injected fsync failure")
	j.sync = func() error { return failed }
	if err := j.append(testEntry()); !errors.Is(err, failed) {
		t.Fatalf("append: %v", err)
	}
}
