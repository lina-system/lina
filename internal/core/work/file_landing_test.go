// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
)

func fileFixture(t *testing.T) (*Service, Workspace, Receipt, *retentionWriter) {
	t.Helper()
	s, _ := testStore(t)
	a, _, _ := areaFixture(t)
	s.area = a
	writer := &retentionWriter{}
	s.materials = writer
	task := taskFixture()
	task.Destination = t.TempDir()
	l := lease.New(func() {})
	defer l.Release()
	w, err := s.Prepare(t.Context(), l, task, WorkspaceSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(w.Path, "out/empty"), 0700); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"out/result.txt": "result", "report.md": "checked report"} {
		writeFixture(t, filepath.Join(w.Path, p), body)
	}
	if err = s.Start(t.Context(), runFixture()); err != nil {
		t.Fatal(err)
	}
	r := fileReceipt(t, w)
	return s, w, r, writer
}
func writeFixture(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func fileReceipt(t *testing.T, w Workspace) Receipt {
	t.Helper()
	root, err := os.OpenRoot(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	tree, err := readTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	r := receiptFixture()
	r.Manifest = nil
	r.Provenance = nil
	for _, f := range tree.Files {
		f.Path = filepath.Join(w.Path, f.Path)
		r.Manifest = append(r.Manifest, f)
		r.Provenance = append(r.Provenance, Provenance{ArtifactID: f.Path, TaskID: w.TaskID, NodeID: "node-1", Path: f.Path, SHA256: f.SHA256, ObservedAt: r.EmittedAt})
	}
	r.RevisionHash = ManifestHash(r.Manifest)
	r.EventID = ReceiptID(r)
	return r
}
func submitFiles(t *testing.T, s *Service, r Receipt) Card {
	t.Helper()
	if err := s.Submit(t.Context(), "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func landFiles(t *testing.T, s *Service, c Card) (FileEvidence, error) {
	t.Helper()
	return s.LandFiles(t.Context(), c.Task.ID, c.Receipt.EventID, c.FileSnapshot.Fingerprint, "Read output and report; the result answers the task.")
}
func TestFileDoneKeepsFingerprintAndSnapshot(t *testing.T) {
	s, w, r, writer := fileFixture(t)
	c := submitFiles(t, s, r)
	if c.FileSnapshot == nil || len(c.Materials) != 2 || c.Applied || c.FileEvidence != nil {
		t.Fatalf("no snapshot or false acceptance: %+v", c)
	}
	old := c.FileSnapshot.Fingerprint
	writeFixture(t, filepath.Join(w.Path, "out/result.txt"), "changed")
	if string(writer.bodies[0]) != "result" {
		t.Fatal("snapshot follows mutable source")
	}
	if _, err := landFiles(t, s, c); !errors.Is(err, ErrFilesChanged) {
		t.Fatalf("changed output: %v", err)
	}
	entries, _ := os.ReadDir(c.Task.Destination)
	if len(entries) != 0 {
		t.Fatal("copied changed output")
	}
	// A new done in the same stopped turn must replace the old snapshot and
	// require a new judgment, without allocating a second worker generation.
	r = fileReceipt(t, w)
	c = submitFiles(t, s, r)
	if c.FileSnapshot.Fingerprint == old || c.Verdict != "" {
		t.Fatal("stale snapshot/judgment")
	}
	if _, err := landFiles(t, s, c); err != nil {
		t.Fatal(err)
	}
}
func TestFileLandingFreshBundleEvidenceAndReplay(t *testing.T) {
	s, _, r, _ := fileFixture(t)
	c := submitFiles(t, s, r)
	existing := filepath.Join(c.Task.Destination, c.Task.ID)
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(existing, "keep"), "existing")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.Task.Destination, c.Task.ID+"-v2")); err != nil {
		t.Fatal(err)
	}
	e, err := landFiles(t, s, c)
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != filepath.Join(c.Task.Destination, c.Task.ID+"-v3") || e.Fingerprint != c.FileSnapshot.Fingerprint || e.Tree.Fingerprint() != e.Fingerprint {
		t.Fatalf("evidence: %+v", e)
	}
	b, _ := os.ReadFile(filepath.Join(existing, "keep"))
	if string(b) != "existing" {
		t.Fatal("overwrote existing bundle")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("followed destination symlink")
	}
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := landFiles(t, s, c)
	if err != nil || again.Path != e.Path {
		t.Fatalf("duplicate landing: %+v %v", again, err)
	}
	card, _ := s.Get(t.Context(), c.Task.ID)
	if !card.Applied || card.State != "done" || len(card.FileCopies) != 1 || card.FileEvidence == nil {
		t.Fatal("missing replayed evidence")
	}
	writeFixture(t, filepath.Join(e.Path, "out/result.txt"), "edited after landing")
	if _, err = landFiles(t, s, card); !errors.Is(err, ErrFilesChanged) {
		t.Fatal("stale destination accepted")
	}
	card, _ = s.Get(t.Context(), c.Task.ID)
	if card.Applied || card.FileEvidence != nil || card.State == "done" {
		t.Fatal("mismatch did not reopen criterion")
	}
}
func TestFileDoneRejectsChangesAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"report", "extra", "deleted", "symlink", "empty-report"} {
		t.Run(kind, func(t *testing.T) {
			s, w, r, _ := fileFixture(t)
			c := submitFiles(t, s, r)
			switch kind {
			case "report":
				writeFixture(t, filepath.Join(w.Path, "report.md"), "different report")
			case "extra":
				writeFixture(t, filepath.Join(w.Path, "out/new"), "new")
			case "deleted":
				if err := os.Remove(filepath.Join(w.Path, "out/result.txt")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("result.txt", filepath.Join(w.Path, "out/link")); err != nil {
					t.Fatal(err)
				}
			case "empty-report":
				writeFixture(t, filepath.Join(w.Path, "report.md"), "")
			}
			if _, err := landFiles(t, s, c); !errors.Is(err, ErrFilesChanged) {
				t.Fatalf("%s accepted: %v", kind, err)
			}
			if err := s.Submit(t.Context(), "run-1", r); err == nil {
				t.Fatal("reused stale done")
			}
		})
	}
}

func replaceRootWithSymlink(t *testing.T, root, name string) {
	t.Helper()
	target := ".saved-" + name
	if name == "report.md" {
		// Both files have identical bytes; the link adds no entry to the tree.
		target = "out/result.txt"
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Rename(filepath.Join(root, name), filepath.Join(root, target)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}

func TestFileDoneRejectsRootSymlinks(t *testing.T) {
	for _, name := range []string{"out", "report.md"} {
		t.Run(name, func(t *testing.T) {
			s, w, r, _ := fileFixture(t)
			writeFixture(t, filepath.Join(w.Path, "report.md"), "result")
			r = fileReceipt(t, w)
			// Keep the exact bytes and receipt; only the root entry becomes a link
			// to a directory or regular file inside the task directory.
			replaceRootWithSymlink(t, w.Path, name)
			if err := s.Submit(t.Context(), "run-1", r); !errors.Is(err, ErrInvalid) {
				t.Errorf("snapshot accepted %s root symlink: %v", name, err)
			}
			c, err := s.Get(t.Context(), "task-1")
			if err != nil {
				t.Fatal(err)
			}
			if c.FileSnapshot != nil || c.Receipt != nil || len(c.Materials) != 0 {
				t.Fatal("symlink snapshot published")
			}
		})
	}
}

type closeHookReader struct {
	io.ReadCloser
	afterClose func()
}

func (r *closeHookReader) Close() error {
	err := r.ReadCloser.Close()
	r.afterClose()
	return err
}

type readbackSymlinkReader struct {
	*retentionWriter
	afterCopy func()
}

func (r *readbackSymlinkReader) Open(ctx context.Context, m Material) (io.ReadCloser, error) {
	input, err := r.retentionWriter.Open(ctx, m)
	if err != nil || m.Path != "report.md" {
		return input, err
	}
	return &closeHookReader{ReadCloser: input, afterClose: r.afterCopy}, nil
}

func TestFileLandingReadbackRejectsRootSymlinks(t *testing.T) {
	for _, name := range []string{"out", "report.md"} {
		t.Run(name, func(t *testing.T) {
			s, w, r, writer := fileFixture(t)
			writeFixture(t, filepath.Join(w.Path, "report.md"), "result")
			r = fileReceipt(t, w)
			c := submitFiles(t, s, r)
			bundle := filepath.Join(c.Task.Destination, c.Task.ID)
			// The final material closes after both output files have been written,
			// but before destination readback. No timing-dependent mutation.
			s.materials = &readbackSymlinkReader{retentionWriter: writer, afterCopy: func() {
				replaceRootWithSymlink(t, bundle, name)
			}}
			if _, err := landFiles(t, s, c); !errors.Is(err, ErrInvalid) {
				t.Errorf("readback accepted %s root symlink: %v", name, err)
			}
			if err := verifyBundle(t.Context(), bundle, c.FileSnapshot.Fingerprint); !errors.Is(err, ErrInvalid) {
				t.Errorf("bundle verification did not reject %s root symlink: %v", name, err)
			}
			card, err := s.Get(t.Context(), c.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if card.Applied || card.FileEvidence != nil || len(card.FileCopies) != 1 || card.FileCopies[0].Status != "partial" {
				t.Fatal("symlink readback published landed evidence")
			}
		})
	}
}

type faultMaterialReader struct {
	*retentionWriter
	onOpen func(Material)
	reads  int
}

func (r *faultMaterialReader) Open(ctx context.Context, m Material) (io.ReadCloser, error) {
	r.reads++
	if r.onOpen != nil {
		r.onOpen(m)
	}
	return r.retentionWriter.Open(ctx, m)
}
func TestFileLandingReadbackMismatchKeepsPartial(t *testing.T) {
	s, _, r, writer := fileFixture(t)
	c := submitFiles(t, s, r)
	// Corrupt the just-written output when the next snapshot file is read. This
	// deterministic seam runs before the actual destination read, with no sleeps.
	s.materials = &faultMaterialReader{retentionWriter: writer, onOpen: func(m Material) {
		if m.Path == "report.md" {
			writeFixture(t, filepath.Join(c.Task.Destination, c.Task.ID, "out/result.txt"), "bad copy")
		}
	}}
	if _, err := landFiles(t, s, c); !errors.Is(err, ErrFilesChanged) {
		t.Fatalf("bad copy: %v", err)
	}
	card, _ := s.Get(t.Context(), c.Task.ID)
	if card.Applied || card.FileEvidence != nil || len(card.FileCopies) != 1 || card.FileCopies[0].Status != "partial" {
		t.Fatalf("partial accepted: %+v", card)
	}
	b, err := os.ReadFile(filepath.Join(card.FileCopies[0].Path, "out/result.txt"))
	if err != nil || string(b) != "bad copy" {
		t.Fatal("partial deleted")
	}
}
func TestFileLandingCrashRecoveryRetainsAndReportsPartial(t *testing.T) {
	s, _, r, writer := fileFixture(t)
	c := submitFiles(t, s, r)
	crashed := errors.New("simulated process loss")
	s.materials = &faultMaterialReader{retentionWriter: writer, onOpen: func(m Material) {
		if m.Path == "report.md" {
			panic(crashed)
		}
	}}
	func() {
		defer func() {
			if got := recover(); got != crashed {
				t.Fatalf("crash seam: %v", got)
			}
		}()
		_, _ = landFiles(t, s, c)
	}()
	before, _ := s.Get(t.Context(), c.Task.ID)
	if before.Applied || before.FileCopies[0].Status != "copying" {
		t.Fatal("crash published landed or failure")
	}
	s = NewWithArea(s.db, s.area, writer)
	if err := s.Recover(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	card, _ := s.Get(t.Context(), c.Task.ID)
	if card.Applied || card.FileEvidence != nil || card.FileCopies[0].Status != "partial" {
		t.Fatal("recovery invented evidence")
	}
	page, _, err := s.SummaryPage(t.Context(), "task-1:1", 0)
	if err != nil || !strings.Contains(page, card.FileCopies[0].Path) || !strings.Contains(page, "interrupted") {
		t.Fatalf("partial not reported: %s %v", page, err)
	}
	if _, err = os.Stat(filepath.Join(card.FileCopies[0].Path, "out/result.txt")); err != nil {
		t.Fatal("partial deleted")
	}
	e, err := landFiles(t, s, card)
	if err != nil || !strings.HasSuffix(e.Path, "-v2") {
		t.Fatalf("fresh retry: %+v %v", e, err)
	}
}
func TestFileLandingCannotUseApplyOrStaleJudgment(t *testing.T) {
	s, _, r, _ := fileFixture(t)
	c := submitFiles(t, s, r)
	if err := s.Judge(t.Context(), c.Task.ID, r.EventID, "verified", "manager checked"); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(t.Context(), c.Task.ID, r.EventID, "worker says copied"); err == nil {
		t.Fatal("claimed evidence applied file task")
	}
	if _, err := s.LandFiles(t.Context(), c.Task.ID, r.EventID, strings.Repeat("0", 64), "checked"); err == nil {
		t.Fatal("stale fingerprint accepted")
	}
}

func TestFileDoneCanReturnToEarlierContent(t *testing.T) {
	s, w, r, _ := fileFixture(t)
	first := submitFiles(t, s, r)
	writeFixture(t, filepath.Join(w.Path, "out/result.txt"), "revision two")
	second := submitFiles(t, s, fileReceipt(t, w))
	writeFixture(t, filepath.Join(w.Path, "out/result.txt"), "result")
	third := submitFiles(t, s, fileReceipt(t, w))
	if third.FileSnapshot.Fingerprint != first.FileSnapshot.Fingerprint || third.FileSnapshot.Revision <= second.FileSnapshot.Revision {
		t.Fatal("old dedup key swallowed a new done")
	}
	if _, err := landFiles(t, s, third); err != nil {
		t.Fatal(err)
	}
}

type changingSnapshotWriter struct {
	*retentionWriter
	change func()
}

func (w *changingSnapshotWriter) Prepare(ctx context.Context, r io.Reader, path, agent string) (MaterialWrite, error) {
	p, e := w.retentionWriter.Prepare(ctx, r, path, agent)
	if path == "report.md" {
		w.change()
	}
	return p, e
}
func TestFileDoneChangesDuringSnapshotLeaveNoRecord(t *testing.T) {
	s, w, r, writer := fileFixture(t)
	s.materials = &changingSnapshotWriter{retentionWriter: writer, change: func() { writeFixture(t, filepath.Join(w.Path, "out/result.txt"), "raced") }}
	if err := s.Submit(t.Context(), "run-1", r); !errors.Is(err, ErrFilesChanged) {
		t.Fatalf("snapshot race: %v", err)
	}
	c, _ := s.Get(t.Context(), "task-1")
	if c.FileSnapshot != nil || c.Receipt != nil || len(c.Materials) != 0 {
		t.Fatal("partial snapshot published")
	}
}
