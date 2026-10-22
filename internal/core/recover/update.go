// SPDX-License-Identifier: Apache-2.0
// Package recover fences interrupted installation transitions before Core opens
// canon. Recovery decisions never infer non-readiness from a timeout.
package recover

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

var ErrUpdateHeld = errors.New("update_held")

// UpdateAction is conservative about any migration lacking an undo proof.
func UpdateAction(r ctlfiles.UpdateRecord) string {
	if r.Phase == "complete" || r.Phase == "rolled_back" {
		return "resume"
	}
	if r.ReachedReady {
		return "hold"
	}
	switch r.Phase {
	case "draining", "drained", "snapshot":
		return "previous"
	}
	if r.ForwardMigration && (r.Phase == "starting" || r.Phase == "booting" || r.Phase == "held") {
		return "hold"
	}
	return "rollback"
}

// UpdateStart runs under core.lock before epoch minting or canon writes. Only
// the one claimed smoke start can enter an unfinished update.
func UpdateStart(s *ctlfiles.Store, root, socket string) error {
	r, err := s.ReadUpdate()
	if err != nil {
		return fmt.Errorf("%w: update_record_invalid", ErrUpdateHeld)
	}
	if r == nil {
		return nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if absolute != r.Root {
		return fmt.Errorf("%w: update_path_changed", ErrUpdateHeld)
	}
	if r.Phase == "complete" || r.Phase == "rolled_back" {
		return nil
	}
	if r.Phase != "starting" || socket != filepath.Join(r.Root, "run/update.sock") {
		return fmt.Errorf("%w: update_interrupted", ErrUpdateHeld)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(executable)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, f)
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != r.CandidateDigest {
		return fmt.Errorf("%w: update_candidate_invalid", ErrUpdateHeld)
	}
	r.Phase = "booting"
	return s.WriteUpdate(*r)
}

// UpdateReady is persisted before publishing READY to any client. Losing the
// observer can never make an already-ready binary eligible for rollback.
func UpdateReady(s *ctlfiles.Store) error {
	r, err := s.ReadUpdate()
	if err != nil || r == nil {
		return err
	}
	if r.Phase == "booting" {
		r.ReachedReady = true
		r.Phase = "ready"
		return s.WriteUpdate(*r)
	}
	return nil
}
