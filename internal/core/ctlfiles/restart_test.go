// SPDX-License-Identifier: Apache-2.0

package ctlfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestartRecordSurvivesReopenWithoutMintingEpoch(t *testing.T) {
	root := privateRoot(t)
	s := openLocked(t, root)
	if _, err := s.MintEpoch(0, 0); err != nil {
		t.Fatal(err)
	}
	if request, err := s.ReadRestart(); err != nil || request != nil {
		t.Fatalf("absent: %+v %v", request, err)
	}
	want := RestartRequest{Schema: 1, ID: "0123456789abcdef0123456789abcdef", Mode: "when_idle", Epoch: "1"}
	if err := s.WriteRestart(want); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openLocked(t, root)
	got, err := s.ReadRestart()
	if err != nil || got == nil || *got != want {
		t.Fatalf("reopened: %+v %v", got, err)
	}
	if epoch, err := s.Epoch(); err != nil || epoch != 1 {
		t.Fatalf("request minted epoch: %d %v", epoch, err)
	}
	want.CommittedEpoch = "1"
	want.DischargedEpoch = "2"
	if err := s.WriteRestart(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReadRestart()
	if err != nil || got == nil || *got != want {
		t.Fatalf("discharged: %+v %v", got, err)
	}
	info, err := os.Stat(filepath.Join(root, "system/core/lifecycle.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestRestartRecordRejectsCorruptionWithoutReplacingIt(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `[]`,
		`{"schema":2,"id":"0123456789abcdef0123456789abcdef","mode":"when_idle","epoch":"1"}`,
		`{"schema":1,"id":"bad","mode":"when_idle","epoch":"1"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"force","epoch":"1"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"01"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"2","committed_epoch":"1"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"2","discharged_epoch":"3"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"2","committed_epoch":"2","discharged_epoch":"2"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"0"}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"1","future":true}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"1","committed_epoch":null}`,
		`{"schema":1,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"1","epoch":"2"}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := openLocked(t, privateRoot(t))
			if err := s.Write("lifecycle.json", []byte(body)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadRestart(); err == nil {
				t.Fatal("invalid restart accepted")
			}
			after, err := s.Read("lifecycle.json")
			if err != nil || string(after) != body {
				t.Fatal("invalid record was changed")
			}
		})
	}
}
