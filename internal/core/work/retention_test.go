// SPDX-License-Identifier: Apache-2.0
package work

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/lease"
)

// The writer fixture still commits metadata in the real canon transaction;
// installed-Core acceptance uses the production materials writer and backup.
type retentionWriter struct {
	bodies [][]byte
	fail   bool
}
type retentionWrite struct {
	owner *retentionWriter
	body  []byte
	path  string
}

func (w *retentionWriter) Prepare(_ context.Context, r io.Reader, path, agent string) (MaterialWrite, error) {
	b, e := io.ReadAll(r)
	return &retentionWrite{w, b, path}, e
}
func (p *retentionWrite) Close() error { return nil }
func (p *retentionWrite) Insert(context.Context, *canon.Tx) (Material, error) {
	if p.owner.fail {
		return Material{}, ErrInvalid
	}
	p.owner.bodies = append(p.owner.bodies, p.body)
	return Material{ID: fmt.Sprint(len(p.owner.bodies)), Revision: 1, Path: p.path, SHA256: fmt.Sprintf("%x", sha256.Sum256(p.body))}, nil
}
func TestRetainResultIsAtomicAndReplayable(t *testing.T) {
	s, _ := testStore(t)
	a, _, _ := areaFixture(t)
	writer := &retentionWriter{}
	s.area = a
	s.materials = writer
	l := lease.New(func() {})
	defer l.Release()
	task := taskFixture()
	task.ArtifactRoots = nil
	w, err := s.Prepare(t.Context(), l, task, WorkspaceSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(t.Context(), runFixture()); err != nil {
		t.Fatal(err)
	}
	body := []byte("unapplied report\n")
	file := filepath.Join(w.Path, "report.txt")
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	receipt := receiptFixture()
	receipt.Manifest[0].Path = file
	receipt.Manifest[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	receipt.Provenance[0].Path = file
	receipt.Provenance[0].SHA256 = receipt.Manifest[0].SHA256
	receipt.RevisionHash = ManifestHash(receipt.Manifest)
	receipt.EventID = ReceiptID(receipt)
	if err = s.Submit(t.Context(), "run-1", receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Retain(t.Context(), l, task.ID, receipt.EventID, ""); err == nil {
		t.Fatal("retained an active writer")
	}
	if err = s.Finish(t.Context(), task.ID, "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	writer.fail = true
	if _, err = s.Retain(t.Context(), l, task.ID, receipt.EventID, ""); err == nil {
		t.Fatal("ignored failed material publication")
	}
	c, _ := s.Get(t.Context(), task.ID)
	if len(c.Materials) != 0 {
		t.Fatal("partial retention event")
	}
	writer.fail = false
	saved, err := s.Retain(t.Context(), l, task.ID, receipt.EventID, "")
	if err != nil || len(saved) != 1 || !bytes.Equal(writer.bodies[0], body) {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if err = os.RemoveAll(w.Path); err != nil {
		t.Fatal(err)
	}
	again, err := s.Retain(t.Context(), l, task.ID, receipt.EventID, "")
	if err != nil || len(again) != 1 || len(writer.bodies) != 1 {
		t.Fatal("retry required lost work area or duplicated material")
	}
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(t.Context(), task.ID)
	if len(c.Materials) != 1 || c.State == "done" || c.Verdict != "" {
		t.Fatal("retention changed acceptance or failed to replay")
	}
}
func TestRetainRejectsChangedBytesAndOutsideLink(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprint(link), func(t *testing.T) {
			s, _ := testStore(t)
			a, _, _ := areaFixture(t)
			writer := &retentionWriter{}
			s.area = a
			s.materials = writer
			l := lease.New(func() {})
			defer l.Release()
			w, err := s.Prepare(t.Context(), l, taskFixture(), WorkspaceSpec{})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Start(t.Context(), runFixture()); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(w.Path, "report.txt")
			if link {
				err = os.Symlink(filepath.Join(t.TempDir(), "outside"), file)
			} else {
				err = os.WriteFile(file, []byte("changed"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			r := receiptFixture()
			r.Manifest[0].Path = file
			r.Provenance[0].Path = file
			r.Provenance[0].ObservedAt = r.EmittedAt
			r.RevisionHash = ManifestHash(r.Manifest)
			r.EventID = ReceiptID(r)
			if err = s.Submit(t.Context(), "run-1", r); err != nil {
				t.Fatal(err)
			}
			if err = s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Retain(t.Context(), l, "task-1", r.EventID, ""); err == nil {
				t.Fatal("unverified bytes retained")
			}
			c, _ := s.Get(t.Context(), "task-1")
			if len(c.Materials) > 0 {
				t.Fatal("published invalid retention")
			}
		})
	}
}
