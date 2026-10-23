// SPDX-License-Identifier: Apache-2.0
package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func testStore(t *testing.T) *Store {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return &Store{DB: db}
}
func TestAdmissionDeduplicatesAndIsolates(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	p := Publication{ID: "publication", Agent: "agent", Conversation: "main:agent"}
	b := Binding{Subject: "one", Client: "client", MaxActive: 2, Expires: now.Add(time.Hour).Unix()}
	d := Definition{ID: "write", Seconds: 60}
	pr := Principal{Subject: b.Subject, Client: b.Client, Expires: b.Expires}
	req := SendRequest{Message: Message{MessageID: "same", Role: "ROLE_USER", Parts: []Part{{Data: []byte(`{"task":"write","input":"hello"}`)}}}}
	first, err := s.Admit(t.Context(), p, b, d, pr, req, now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			r, e := s.Admit(t.Context(), p, b, d, pr, req, now)
			if e != nil || r.Task.ID != first.Task.ID {
				t.Errorf("retry: %v %v", r, e)
			}
		})
	}
	wg.Wait()
	changed := req
	changed.Message.Parts = []Part{{Data: []byte(`{"task":"write","input":"changed"}`)}}
	if _, err = s.Admit(t.Context(), p, b, d, pr, changed, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed duplicate: %v", err)
	}
	other := pr
	other.Subject = "two"
	if _, err = s.Get(t.Context(), p.ID, other.Key(), first.Task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross caller get: %v", err)
	}
	rows, err := s.List(t.Context(), p.ID, other.Key())
	if err != nil || len(rows) != 0 {
		t.Fatalf("cross caller list: %v %v", rows, err)
	}
	rows, err = s.List(t.Context(), p.ID, pr.Key())
	if err != nil || len(rows) != 1 {
		t.Fatalf("admitted %d times: %v", len(rows), err)
	}
	if first.Task.ContextID == p.Conversation || first.Task.ID == "same" {
		t.Fatal("public identity is not opaque")
	}
}
func TestRecoveryDoesNotRequeueClaimedExecution(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	p := Publication{ID: "p"}
	b := Binding{Subject: "s", Client: "c", MaxActive: 1}
	d := Definition{Seconds: 60}
	pr := Principal{Subject: "s", Client: "c", Expires: now.Add(time.Hour).Unix()}
	r, err := s.Admit(t.Context(), p, b, d, pr, SendRequest{Message: Message{MessageID: "m"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Claim(t.Context(), r.Task.ID); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, err := s.Claim(t.Context(), r.Task.ID); err != nil || ok {
		t.Fatalf("second claim: %v %v", ok, err)
	}
	if err = s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Get(t.Context(), p.ID, pr.Key(), r.Task.ID)
	if err != nil || recovered.Phase != "unknown" || recovered.Task.Status.State == Completed {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	if ok, err := s.Claim(t.Context(), r.Task.ID); err != nil || ok {
		t.Fatalf("replayed after restart: %v %v", ok, err)
	}
}

func TestAdmissionHashCanonicalizesData(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	p := Publication{ID: "p"}
	b := Binding{MaxActive: 2}
	d := Definition{Seconds: 60}
	pr := Principal{Subject: "s", Client: "c"}
	req := SendRequest{Message: Message{MessageID: "one", Role: "ROLE_USER", Parts: []Part{{Data: []byte(`{"task":"write","input":"same"}`)}}}}
	first, err := s.Admit(t.Context(), p, b, d, pr, req, now)
	if err != nil {
		t.Fatal(err)
	}
	req.Message.Parts[0].Data = []byte(`{ "input":"same", "task":"write" }`)
	second, err := s.Admit(t.Context(), p, b, d, pr, req, now)
	if err != nil || first.Task.ID != second.Task.ID {
		t.Fatalf("equivalent retry conflicted: %v", err)
	}
}

func TestFollowUpKeepsTaskAuthorityAndCannotReopenTerminalWork(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	p := Publication{ID: "p", Agent: "agent"}
	b := Binding{MaxActive: 2}
	d := Definition{ID: "write", Seconds: 60}
	pr := Principal{Subject: "one", Client: "client"}
	req := SendRequest{Message: Message{MessageID: "first", Role: "ROLE_USER", Parts: []Part{{Data: []byte(`{"task":"write","input":"first"}`)}}}}
	first, err := s.Admit(t.Context(), p, b, d, pr, req, now)
	if err != nil {
		t.Fatal(err)
	}
	req.Message.MessageID = "follow"
	req.Message.TaskID = first.Task.ID
	req.Message.ContextID = first.Task.ContextID
	req.Message.Parts[0].Data = []byte(`{"task":"write","input":"additional data"}`)
	follow, err := s.Admit(t.Context(), p, b, d, pr, req, now)
	if err != nil || follow.Task.ID != first.Task.ID || len(follow.Task.History) != 2 {
		t.Fatalf("follow-up: %+v %v", follow, err)
	}
	if follow.Deadline != first.Deadline || follow.Authority != first.Authority {
		t.Fatal("follow-up extended authority")
	}
	other := pr
	other.Subject = "other"
	if _, err = s.Admit(t.Context(), p, b, d, other, req, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross caller follow-up: %v", err)
	}
	if _, err = s.Update(t.Context(), first.Task.ID, func(r *Record) error { SetState(r, Completed, now); return nil }); err != nil {
		t.Fatal(err)
	}
	req.Message.MessageID = "reopen"
	if _, err = s.Admit(t.Context(), p, b, d, pr, req, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("terminal task reopened: %v", err)
	}
}
