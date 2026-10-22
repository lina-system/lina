// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type FileCopy struct {
	Attempt     int    `json:"attempt"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Problem     string `json:"problem,omitempty"`
}
type FileEvidence struct {
	Path        string   `json:"path"`
	Fingerprint string   `json:"fingerprint"`
	Tree        FileTree `json:"tree"`
}

// RecoveredFileResult reads the recovered ledger in authority's transaction.
// An interrupted copy must already be reported before its invocation's target
// reservation can be released. This does not claim fresh destination evidence.
func RecoveredFileResult(ctx context.Context, tx *canon.Tx, id string) (Card, string, error) {
	c, err := taskCard(ctx, tx, id)
	if err != nil {
		return Card{}, "", err
	}
	if c.Task.Destination == "" {
		return Card{}, "", ErrInvalid
	}
	for _, copy := range c.FileCopies {
		if copy.Status == "copying" {
			return Card{}, "", ErrState
		}
	}
	raw, err := json.Marshal(c)
	return c, digest(string(raw)), err
}

func advanceFiles(c *Card, e Event) error {
	switch e.Kind {
	case "file_receipt":
		if c.Task.Destination == "" || c.Applied || c.Run == nil || c.Run.ID != e.RunID || e.Receipt == nil || e.FileSnapshot == nil {
			return ErrState
		}
		if err := validateReceipt(*c, *e.Receipt); err != nil {
			return err
		}
		if e.FileSnapshot.Fingerprint != e.FileSnapshot.Tree.Fingerprint() || len(e.FileSnapshot.Materials) != len(e.FileSnapshot.Tree.Files) {
			return ErrInvalid
		}
		c.Receipt = e.Receipt
		c.FileSnapshot = e.FileSnapshot
		c.Materials = e.FileSnapshot.Materials
		c.FileEvidence = nil
		c.Verdict = ""
		c.Applied = false
		c.Stages[0] = Stage{Name: "worker", Source: "worker_report", Summary: e.Receipt.Outcome}
		c.Stages[1] = Stage{Name: "review", Source: "unknown"}
		c.Stages[2] = Stage{Name: "apply", Source: "unknown"}
		c.State = "working"
	case "copy_started":
		if c.Active || c.FileSnapshot == nil || c.Verdict != "verified" || c.Applied || e.FileCopy == nil || e.FileCopy.Status != "copying" || e.FileCopy.Fingerprint != c.FileSnapshot.Fingerprint {
			return ErrState
		}
		if len(c.FileCopies) > 0 && c.FileCopies[len(c.FileCopies)-1].Status == "copying" {
			return ErrState
		}
		c.FileCopies = append(c.FileCopies, *e.FileCopy)
		c.Stages[2] = Stage{Name: "apply", Source: "core_file_read", Summary: "copying to " + e.FileCopy.Path}
	case "copy_failed", "files_landed":
		if len(c.FileCopies) == 0 || e.FileCopy == nil {
			return ErrState
		}
		i := len(c.FileCopies) - 1
		if c.FileCopies[i].Path != e.FileCopy.Path || c.FileCopies[i].Status != "copying" {
			return ErrState
		}
		if e.Kind == "files_landed" {
			if e.FileEvidence == nil || c.FileSnapshot == nil || e.FileEvidence.Path != e.FileCopy.Path || e.FileEvidence.Fingerprint != c.FileSnapshot.Fingerprint || e.FileEvidence.Tree.Fingerprint() != e.FileEvidence.Fingerprint {
				return ErrInvalid
			}
			c.FileEvidence = e.FileEvidence
			c.Applied = true
			c.State = "done"
			c.Stages[2] = Stage{Name: "apply", Source: "core_file_read", Summary: "verified at " + e.FileCopy.Path}
		} else {
			c.Applied = false
			c.FileEvidence = nil
			c.State = "ready_for_review"
			c.Stages[2] = Stage{Name: "apply", Source: "core_file_read", Summary: e.FileCopy.Problem + ": " + e.FileCopy.Path}
		}
		c.FileCopies[i] = *e.FileCopy
	case "files_invalidated":
		c.Applied = false
		c.FileEvidence = nil
		c.State = "ready_for_review"
		c.Stages[2] = Stage{Name: "apply", Source: "core_file_read", Summary: e.Evidence}
	default:
		return ErrInvalid
	}
	return nil
}

// LandFiles records the manager's judgment of this exact snapshot, then performs
// the mechanical handover. A model cannot supply file evidence or a new target.
func (s *Service) LandFiles(ctx context.Context, taskID, receiptID, fingerprint, judgment string) (FileEvidence, error) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	c, err := s.Get(ctx, taskID)
	if err != nil {
		return FileEvidence{}, err
	}
	if c.Active || c.TurnStatus != "completed" || c.Receipt == nil || c.Receipt.EventID != receiptID || c.FileSnapshot == nil || fingerprint != c.FileSnapshot.Fingerprint || judgment == "" {
		return FileEvidence{}, ErrState
	}
	if c.FileEvidence != nil {
		err = verifyBundle(ctx, c.FileEvidence.Path, fingerprint)
		if err == nil {
			return *c.FileEvidence, nil
		}
		if e := s.append(ctx, Event{Key: fmt.Sprintf("invalidate:%s:%d", receiptID, len(c.FileCopies)), TaskID: taskID, Kind: "files_invalidated", Evidence: "destination changed; file criterion remains open"}); e != nil {
			return FileEvidence{}, e
		}
		return FileEvidence{}, ErrFilesChanged
	}
	if err = s.verifyLive(ctx, c); err != nil {
		return FileEvidence{}, err
	}
	if c.Verdict == "" {
		if err = s.append(ctx, Event{Key: fmt.Sprintf("file-judge:%s:%s:%d", taskID, receiptID, c.FileSnapshot.Revision), TaskID: taskID, Kind: "judged", ReceiptID: receiptID, Value: "verified", Evidence: judgment}); err != nil {
			return FileEvidence{}, err
		}
		c, err = s.Get(ctx, taskID)
		if err != nil {
			return FileEvidence{}, err
		}
	}
	if c.Verdict != "verified" {
		return FileEvidence{}, ErrState
	}
	return s.copyFiles(ctx, c)
}

func (s *Service) verifyLive(ctx context.Context, c Card) error {
	root, err := s.sourceRoot(c)
	if err != nil {
		return errors.Join(ErrFilesChanged, err)
	}
	defer root.Close()
	tree, err := readTree(ctx, root)
	if err != nil || tree.Fingerprint() != c.FileSnapshot.Fingerprint {
		return errors.Join(ErrFilesChanged, err)
	}
	return nil
}

func verifyBundle(ctx context.Context, path, fingerprint string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	tree, err := readTree(ctx, root)
	if err != nil {
		return err
	}
	// A bundle contains only the result tree. Extra top-level entries also
	// invalidate evidence, even though they are not deliverables.
	entries, err := fsEntries(root)
	if err != nil {
		return err
	}
	if len(entries) != 2 || tree.Fingerprint() != fingerprint {
		return ErrFilesChanged
	}
	return nil
}
func fsEntries(root *os.Root) ([]os.DirEntry, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (s *Service) copyFiles(ctx context.Context, c Card) (evidence FileEvidence, err error) {
	reader, ok := s.materials.(MaterialReader)
	if !ok {
		return evidence, ErrInvalid
	}
	// Resolve both sides before checking containment; aliases cannot put the
	// destination inside Core's work or storage directories.
	destination, err := resolveDestination(c.Task.Destination)
	if err != nil {
		return evidence, err
	}
	state, err := filepath.EvalSymlinks(filepath.Dir(s.area.path))
	if err != nil {
		return evidence, err
	}
	if inside(destination, state) {
		return evidence, ErrInvalid
	}
	if err = os.MkdirAll(destination, 0700); err != nil {
		return evidence, err
	}
	parent, err := os.OpenRoot(destination)
	if err != nil {
		return evidence, err
	}
	defer parent.Close()
	var copy FileCopy
	var name string
	for version := 1; ; version++ {
		if err = ctx.Err(); err != nil {
			return evidence, err
		}
		name = c.Task.ID
		if version > 1 {
			name += "-v" + strconv.Itoa(version)
		}
		if _, e := parent.Lstat(name); e == nil {
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return evidence, e
		}
		// The intent precedes mkdir. A crash in either operation has a durable
		// path to report. No recovery ever deletes or resumes a partial bundle.
		copy = FileCopy{Attempt: len(c.FileCopies) + 1, Path: filepath.Join(destination, name), Fingerprint: c.FileSnapshot.Fingerprint, Status: "copying"}
		key := fmt.Sprintf("copy:%s:%d", c.Task.ID, copy.Attempt)
		if err = s.append(ctx, Event{Key: key, TaskID: c.Task.ID, Kind: "copy_started", FileCopy: &copy}); err != nil {
			return evidence, err
		}
		err = parent.Mkdir(name, 0700)
		if err == nil {
			break
		}
		if e := s.failCopy(ctx, c.Task.ID, copy, "destination claim failed; any files retained"); e != nil {
			return evidence, errors.Join(err, e)
		}
		if !errors.Is(err, os.ErrExist) {
			return evidence, err
		}
		c.FileCopies = append(c.FileCopies, copy)
	}
	defer func() {
		if err != nil {
			problem := "copy incomplete; partial files retained"
			// Use the shutdown-independent context for the small durable failure
			// record. A process crash is handled by Recover instead.
			err = errors.Join(fmt.Errorf("%s at %s: %w", problem, copy.Path, err), s.failCopy(context.WithoutCancel(ctx), c.Task.ID, copy, problem))
		}
	}()
	bundle, err := parent.OpenRoot(name)
	if err != nil {
		return evidence, err
	}
	defer bundle.Close()
	for _, dir := range c.FileSnapshot.Tree.Directories {
		if err = bundle.Mkdir(dir, 0700); err != nil {
			return evidence, err
		}
	}
	for i, file := range c.FileSnapshot.Tree.Files {
		m := c.FileSnapshot.Materials[i]
		if m.Path != file.Path || m.SHA256 != file.SHA256 {
			return evidence, ErrFilesChanged
		}
		input, e := reader.Open(ctx, m)
		if e != nil {
			return evidence, e
		}
		output, e := bundle.OpenFile(file.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			input.Close()
			return evidence, e
		}
		_, e = io.Copy(output, contextFileReader{ctx, input})
		e = errors.Join(e, output.Sync(), output.Close(), input.Close())
		if e != nil {
			return evidence, e
		}
	}
	// Sync directory entries before committing landed; process interruption can
	// never publish a record for an incomplete or unverified copy.
	for i := len(c.FileSnapshot.Tree.Directories) - 1; i >= 0; i-- {
		if err = syncDir(bundle, c.FileSnapshot.Tree.Directories[i]); err != nil {
			return evidence, err
		}
	}
	if err = syncDir(bundle, "."); err != nil {
		return evidence, err
	}
	if err = syncDir(parent, "."); err != nil {
		return evidence, err
	}
	if err = s.verifyLive(ctx, c); err != nil {
		return evidence, err
	}
	if err = verifyBundle(ctx, copy.Path, copy.Fingerprint); err != nil {
		return evidence, err
	}
	// Record the actual second read, not the expected manifest or worker claim.
	readback, err := os.OpenRoot(copy.Path)
	if err != nil {
		return evidence, err
	}
	tree, err := readTree(ctx, readback)
	err = errors.Join(err, readback.Close())
	if err != nil {
		return evidence, err
	}
	if tree.Fingerprint() != copy.Fingerprint {
		return evidence, ErrFilesChanged
	}
	evidence = FileEvidence{Path: copy.Path, Fingerprint: tree.Fingerprint(), Tree: tree}
	copy.Status = "landed"
	err = s.append(ctx, Event{Key: fmt.Sprintf("landed:%s:%d", c.Task.ID, copy.Attempt), TaskID: c.Task.ID, Kind: "files_landed", FileCopy: &copy, FileEvidence: &evidence})
	return evidence, err
}
func syncDir(root *os.Root, path string) error {
	f, e := root.Open(path)
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}
func (s *Service) failCopy(ctx context.Context, id string, copy FileCopy, problem string) error {
	copy.Status = "partial"
	copy.Problem = problem
	return s.append(ctx, Event{Key: fmt.Sprintf("copy-failed:%s:%d", id, copy.Attempt), TaskID: id, Kind: "copy_failed", FileCopy: &copy})
}

// Recovery reports every interrupted intent, including an absent directory or
// a fully copied directory whose final transaction did not commit. It never
// invents landed evidence or removes the person's files.
func (s *Service) recoverFileCopies(ctx context.Context) error {
	var cards []Card
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		ids, e := ListReferences(ctx, tx)
		if e != nil {
			return e
		}
		for _, id := range ids {
			c, e := ReadReference(ctx, tx, id)
			if e != nil {
				return e
			}
			cards = append(cards, c)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range cards {
		for _, copy := range c.FileCopies {
			if copy.Status == "copying" {
				problem := "interrupted copy; partial files retained"
				if _, e := os.Lstat(copy.Path); errors.Is(e, os.ErrNotExist) {
					problem = "interrupted before destination creation; no landed result"
				}
				if err = s.failCopy(ctx, c.Task.ID, copy, problem); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Resolve missing trailing directories through their existing ancestor before
// creating anything, so a refused destination cannot modify Core's own state.
func resolveDestination(path string) (string, error) {
	if !absolute(path) {
		return "", ErrInvalid
	}
	ancestor := path
	var suffix []string
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	return resolved, nil
}
