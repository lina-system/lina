// SPDX-License-Identifier: Apache-2.0
package ctlfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateReadyCannotBeErased(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AcquireUpdate(); err != nil {
		t.Fatal(err)
	}
	r := UpdateRecord{Schema: 1, ID: "attempt", Root: root, Socket: filepath.Join(root, "run/client.sock"), Phase: "ready", Candidate: filepath.Join(root, "system/updates/candidate"), Previous: filepath.Join(root, "system/updates/previous-attempt"), ReachedReady: true}
	if err = s.WriteUpdate(r); err != nil {
		t.Fatal(err)
	}
	r.ReachedReady = false
	r.Phase = "starting"
	if err = s.WriteUpdate(r); err == nil {
		t.Fatal("READY was erased")
	}
	got, err := s.ReadUpdate()
	if err != nil || !got.ReachedReady {
		t.Fatal("READY lost", err)
	}
}
func TestUpdaterLockIsNotCanonAuthority(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AcquireUpdate(); err != nil {
		t.Fatal(err)
	}
	if s.Locked() {
		t.Fatal("update coordinator gained canon writer authority")
	}
	if err = s.Write("epoch", []byte("42")); err == nil {
		t.Fatal("update coordinator minted epoch")
	}
}
