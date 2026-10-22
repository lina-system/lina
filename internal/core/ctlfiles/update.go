// SPDX-License-Identifier: Apache-2.0
package ctlfiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// UpdateRecord survives both backup restore and updater death. Only the updater
// writes it except while a candidate owns Core's lock: that process records its
// boot claim and READY before exposing admission. The updater joins it before
// making another transition. ReachedReady is sticky for the whole attempt.
type UpdateRecord struct {
	Schema           int    `json:"schema"`
	ID               string `json:"id"`
	Root             string `json:"root"`
	Socket           string `json:"socket"`
	Phase            string `json:"phase"`
	Candidate        string `json:"candidate"`
	Previous         string `json:"previous"`
	Generation       string `json:"generation,omitempty"`
	CandidateDigest  string `json:"candidate_digest,omitempty"`
	PreviousDigest   string `json:"previous_digest,omitempty"`
	ForwardMigration bool   `json:"forward_migration"`
	ReachedReady     bool   `json:"reached_ready"`
	Cause            string `json:"cause,omitempty"`
}

func (r UpdateRecord) validate() error {
	if r.Schema != 1 || r.ID == "" || !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root || !filepath.IsAbs(r.Socket) || r.Candidate == "" || r.Previous == "" {
		return errors.New("update_record_invalid")
	}
	if filepath.Base(r.ID) != r.ID || r.ID == "." || r.ID == ".." {
		return errors.New("update_record_invalid")
	}
	if r.Previous != filepath.Join(r.Root, "system/updates", "previous-"+r.ID) || filepath.Dir(r.Candidate) != filepath.Join(r.Root, "system/updates") {
		return errors.New("update_record_invalid")
	}
	if r.Generation != "" && filepath.Dir(r.Generation) != filepath.Join(r.Root, "backups") {
		return errors.New("update_record_invalid")
	}
	switch r.Phase {
	case "draining", "drained", "snapshot", "swapping", "starting", "booting", "ready", "complete", "rollback", "rolled_back", "held":
	default:
		return errors.New("update_record_invalid")
	}
	switch r.Cause {
	case "", "update_interrupted", "update_path_changed", "update_snapshot_failed", "update_incompatible_backup", "update_swap_failed", "update_start_failed", "update_smoke_failed", "update_resume_failed", "update_irreversible_migration", "update_rollback_failed", "update_disk_full", "update_candidate_invalid", "update_drain_failed":
	default:
		return errors.New("update_record_invalid")
	}
	if (r.Phase == "ready" || r.Phase == "complete") && !r.ReachedReady {
		return errors.New("update_record_invalid")
	}
	return nil
}

func (s *Store) ReadUpdate() (*UpdateRecord, error) {
	data, err := s.Read("update.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Reject duplicate or null fields, which could erase the sticky READY bit.
	fields := json.NewDecoder(bytes.NewReader(data))
	if token, err := fields.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("update_record_invalid")
	}
	seen := map[string]bool{}
	for fields.More() {
		token, err := fields.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || seen[name] {
			return nil, errors.New("update_record_invalid")
		}
		seen[name] = true
		var value json.RawMessage
		if err := fields.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errors.New("update_record_invalid")
		}
	}
	for _, name := range []string{"schema", "id", "root", "socket", "phase", "candidate", "previous", "forward_migration", "reached_ready"} {
		if !seen[name] {
			return nil, errors.New("update_record_invalid")
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var r UpdateRecord
	if err := d.Decode(&r); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("update_record_invalid")
	}
	return &r, r.validate()
}
func (s *Store) WriteUpdate(r UpdateRecord) error {
	if err := r.validate(); err != nil {
		return err
	}
	old, err := s.ReadUpdate()
	if err != nil {
		return err
	}
	if old != nil && old.ID == r.ID && old.ReachedReady && !r.ReachedReady {
		return errors.New("update_ready_is_irreversible")
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.Write("update.json", data)
}
