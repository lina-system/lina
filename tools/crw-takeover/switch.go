// SPDX-License-Identifier: Apache-2.0
// Package takeover implements an operator procedure, excluded from LINA releases.
package takeover

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Manifest is the owner-verified transfer snapshot. Coverage names the inventory
// and confirmation that all non-service directors are quiescent. It is hashed
// into the approval, not inferred from the daemon's running flag.
type Manifest struct {
	SwitchID       string   `json:"switch_id"`
	Tasks          []string `json:"tasks"`
	Snapshot       string   `json:"import_snapshot_sha256"`
	Coverage       string   `json:"quiesced_directors"`
	Executable     string   `json:"executable"`
	StateDirectory string   `json:"state_directory"`
	Socket         string   `json:"app_server_socket"`
}

func (m Manifest) Digest() string {
	b, _ := json.Marshal(m)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

type Observation struct {
	Enabled        *bool  `json:"enabled"`
	Running        *bool  `json:"running"`
	Lock           string `json:"lock"`
	StateDirectory string `json:"stateDirectory"`
	ScopeAuthority string `json:"scopeAuthority"`
	Conflicts      []any  `json:"conflicts"`
}

func encodeObservation(o Observation) []byte { b, _ := json.Marshal(o); return b }
func (o Observation) valid(m Manifest) bool {
	return o.Enabled != nil && o.Running != nil && (!*o.Running || *o.Enabled) && o.StateDirectory == m.StateDirectory && o.ScopeAuthority == "production" && o.Conflicts != nil && len(o.Conflicts) == 0 && ((*o.Running && o.Lock == "held") || (!*o.Running && o.Lock == "free"))
}

type Journal struct {
	Digest string       `json:"manifest_sha256"`
	Before *Observation `json:"before,omitempty"`
	Steps  []string     `json:"steps"`
}
type Procedure struct {
	Manifest Manifest
	Journal  string
	Core     func(context.Context, protocol.WorkRequest) (protocol.WorkResult, error)
	Command  func(context.Context, string) ([]byte, error)
}

func (p Procedure) lock() (*os.File, error) {
	f, err := os.OpenFile(p.Journal+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another switch operator holds the journal")
	}
	return f, nil
}
func (p Procedure) save(j Journal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.Journal), ".switch-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), p.Journal); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(p.Journal))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (p Procedure) step(j *Journal, step string) error {
	j.Steps = append(j.Steps, step)
	return p.save(*j)
}
func (p Procedure) request(ctx context.Context, operation, evidence string) (protocol.WorkResult, error) {
	q := protocol.WorkRequest{Operation: operation, SwitchID: p.Manifest.SwitchID}
	if evidence != "" {
		q.Evidence = &evidence
	}
	return p.Core(ctx, q)
}
func (p Procedure) observe(ctx context.Context) (Observation, error) {
	var o Observation
	raw, err := p.Command(ctx, "status")
	if err != nil {
		return o, err
	}
	if err = json.Unmarshal(raw, &o); err != nil {
		return o, err
	}
	if !o.valid(p.Manifest) {
		return o, errors.New("external ownership or coverage is unknown")
	}
	return o, nil
}
func (p Procedure) Apply(ctx context.Context) error {
	lock, err := p.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	v, err := p.request(ctx, "switch_status", "")
	if err != nil {
		return err
	}
	tasks := append([]string{}, p.Manifest.Tasks...)
	// Manifest order is canonicalized by the CLI; reject mismatched task sets.
	if v.State != "approved" || v.Approval == "" || v.Snapshot != p.Manifest.Digest() || !reflect.DeepEqual(v.Tasks, tasks) || strings.TrimSpace(p.Manifest.Coverage) == "" {
		return errors.New("matching owner approval and quiescence inventory required")
	}
	if _, err = os.Lstat(p.Journal); err == nil {
		return errors.New("journal exists; inspect and use explicit rollback")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	j := Journal{Digest: p.Manifest.Digest(), Steps: []string{}}
	before, err := p.observe(ctx)
	if err != nil {
		return err
	}
	j.Before = &before
	if err = p.step(&j, "disable_requested"); err != nil {
		return err
	}
	_, err = p.Command(ctx, "disable")
	if err == nil {
		var stopped Observation
		stopped, err = p.observe(ctx)
		if err == nil && (*stopped.Enabled || *stopped.Running) {
			err = errors.New("external coordinator is not disabled and stopped")
		}
	}
	if err == nil {
		err = p.step(&j, "external_quiesced")
	}
	if err == nil {
		_, err = p.request(ctx, "activate_switch", "operator:"+j.Digest)
	}
	if err == nil {
		return p.step(&j, "active")
	}
	// Rollback is a recorded transition, never deletion or a silent retry. A
	// canceled call leaves a journal for the explicit rollback command.
	return errors.Join(err, p.rollback(ctx, &j))
}
func (p Procedure) Rollback(ctx context.Context) error {
	lock, err := p.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	raw, err := os.ReadFile(p.Journal)
	if err != nil {
		return err
	}
	var j Journal
	if err = json.Unmarshal(raw, &j); err != nil {
		return err
	}
	if j.Digest != p.Manifest.Digest() || j.Before == nil || !j.Before.valid(p.Manifest) {
		return errors.New("journal does not match the approved manifest")
	}
	return p.rollback(ctx, &j)
}
func (p Procedure) rollback(ctx context.Context, j *Journal) error {
	if err := p.step(j, "rollback_requested"); err != nil {
		return err
	}
	v, err := p.request(ctx, "switch_status", "")
	if err != nil {
		return err
	}
	if v.Snapshot != j.Digest {
		return errors.New("Core switch differs from rollback journal")
	}
	if v.State != "rolled_back" {
		if v.State != "rollback_pending" {
			if _, err = p.request(ctx, "rollback_switch", "operator rollback:"+j.Digest); err != nil {
				return err
			}
		}
		if _, err = p.request(ctx, "release_switch", "operator drain:"+j.Digest); err != nil {
			return err
		}
	}
	// CompleteRollback refuses active or unknown LINA runs. Only its durable
	// release authorizes restoration of the prior coordinator.
	if err = p.step(j, "lina_released"); err != nil {
		return err
	}
	if j.Before == nil {
		return errors.New("original service state unavailable")
	}
	action := "disable"
	if *j.Before.Enabled {
		action = "enable"
	}
	if _, err = p.Command(ctx, action); err != nil {
		return err
	}
	if !*j.Before.Running && *j.Before.Enabled {
		if _, err = p.Command(ctx, "stop"); err != nil {
			return err
		}
	}
	if *j.Before.Running {
		current, e := p.observe(ctx)
		if e != nil {
			return e
		}
		if !*current.Running {
			if _, err = p.Command(ctx, "start"); err != nil {
				return err
			}
		}
	}
	restored, err := p.observe(ctx)
	if err != nil {
		return err
	}
	if *restored.Enabled != *j.Before.Enabled || *restored.Running != *j.Before.Running {
		return errors.New("external restoration unverified")
	}
	return p.step(j, "rolled_back")
}
