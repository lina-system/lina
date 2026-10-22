// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/recover"
	"github.com/thisisjun786/lina/internal/core/update"
	"github.com/thisisjun786/lina/kit/protocol"
)

func activateUpdate(ctx context.Context, root, candidate string, resume bool) (ctlfiles.UpdateRecord, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	store, err := ctlfiles.Open(root)
	if err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	defer store.Close()
	if err = store.AcquireUpdate(); err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	prior, err := store.ReadUpdate()
	if err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	if !resume && prior != nil && prior.Phase != "complete" && prior.Phase != "rolled_back" {
		return *prior, errors.New("update_held: resume the pending update")
	}
	raw, err := os.ReadFile(filepath.Join(root, "system/install.json"))
	if err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	var installed install.Result
	if err = json.Unmarshal(raw, &installed); err != nil || installed.Socket == "" {
		return ctlfiles.UpdateRecord{}, errors.New("update_path_changed")
	}
	host := install.NewProcessHost()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		host.Close(cleanup)
	}()
	manager, err := host.LookPath("systemctl")
	if err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	// Inspect the loaded unit before controlling it. A foreign unit/drop-in is
	// never stopped, even when it happens to have the same service name.
	if err = verifyUpdateService(ctx, host, manager, root, installed.Socket); err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	service := func(ctx context.Context, verb string) error {
		_, err := host.Run(ctx, manager, "--user", verb, "lina-core.service")
		return err
	}
	if resume {
		if prior == nil {
			return ctlfiles.UpdateRecord{}, errors.New("no_pending_update")
		}
		if prior.Root != root || prior.Socket != installed.Socket {
			return *prior, errors.New("update_path_changed")
		}
		return resumeUpdate(ctx, store, *prior, service)
	}
	preflight, err := host.Run(ctx, filepath.Join(root, "system/bin/lina"), "core", "--preflight", "--state-root", root)
	if err != nil {
		return ctlfiles.UpdateRecord{}, err
	}
	var previousProtocol update.PreflightResult
	if json.Unmarshal(preflight, &previousProtocol) != nil || previousProtocol.UpdateProtocol != 1 {
		return ctlfiles.UpdateRecord{}, errors.New("update_protocol_unsupported")
	}
	r, err := update.StageActivation(ctx, root, installed.Socket, candidate)
	if err != nil {
		return r, err
	}
	// Staging is still reversible and cannot stop the active composition.
	ops := update.ActivationOps{
		Drain: func(ctx context.Context) error {
			if err := drainCore(ctx, installed.Socket); err != nil {
				return err
			}
			// Never send service stop until the fenced Core has exited and released
			// its lock. A busy/unknown registry is waited for without a forced timeout.
			if err := waitCoreStopped(ctx, root, store); err != nil {
				return err
			}
			return service(ctx, "stop")
		},
		Snapshot: func(ctx context.Context) (string, error) {
			output, err := host.RunWithContext(ctx, filepath.Join(root, "system/bin/lina"), "backup", "--state-root", root)
			if err != nil {
				return "", err
			}
			path, ok := strings.CutPrefix(strings.TrimSpace(string(output)), "Verified backup: ")
			if !ok || filepath.Dir(path) != filepath.Join(root, "backups") {
				return "", errors.New("update_incompatible_backup")
			}
			verified, err := backup.Verify(ctx, path)
			if err != nil {
				return "", err
			}
			current, err := manifest.Read(filepath.Join(root, "system/bin/manifest.json"))
			if err != nil {
				return "", err
			}
			if verified.Artifacts["lina"] != current.Version+"@"+current.Commit {
				return "", errors.New("update_incompatible_backup")
			}
			data, err := host.Run(ctx, filepath.Join(candidate, "lina"), "core", "--preflight", "--state-root", root, "--manifest", filepath.Join(candidate, "manifest.json"), "--required-manifest", filepath.Join(root, "system/bin/manifest.json"))
			if err != nil {
				return "", err
			}
			var checked update.PreflightResult
			if json.Unmarshal(data, &checked) != nil || checked.StoreSchema != verified.SQLite.SchemaVersion || checked.BinarySchema < checked.StoreSchema || checked.UpdateProtocol != 1 || (checked.SchemaRelation == "forward") != r.ForwardMigration {
				return "", errors.New("update_incompatible_backup")
			}
			return path, nil
		},
		Swap: func(ctx context.Context) error {
			return withStoppedCore(root, func() error { return update.Swap(r, nil) })
		},
		Smoke: func(ctx context.Context) error { return smokeUpdate(ctx, store, r) },
		Rollback: func(ctx context.Context) error {
			current, err := store.ReadUpdate()
			if err != nil {
				return err
			}
			return rollbackUpdate(ctx, *current)
		},
		Resume: func(ctx context.Context) error {
			if err := service(ctx, "start"); err != nil {
				return err
			}
			if err := waitUpdateClient(ctx, installed.Socket); err != nil {
				_ = service(ctx, "stop")
				return err
			}
			return nil
		},
	}
	return update.Activate(ctx, store, r, ops)
}

func verifyUpdateService(ctx context.Context, host install.Host, manager, root, socket string) error {
	data, err := host.Run(ctx, manager, "--user", "show", "lina-core.service", "--property=DropInPaths", "--value")
	if err != nil || strings.TrimSpace(string(data)) != "" {
		return errors.New("update_foreign_service")
	}
	data, err = host.Run(ctx, manager, "--user", "show", "lina-core.service", "--property=FragmentPath", "--value")
	if err != nil {
		return err
	}
	path := strings.TrimSpace(string(data))
	if !filepath.IsAbs(path) {
		return errors.New("update_foreign_service")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !install.MatchesCoreUnit(string(raw), root, socket) {
		return errors.New("update_path_changed")
	}
	return nil
}
func drainCore(ctx context.Context, socket string) error {
	c, err := clientapi.DialWithCapabilities(ctx, socket, "update", []protocol.Capability{{Name: "lifecycle", Version: "1.0"}})
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.Restart(ctx, protocol.RestartModeDrain); err != nil {
		return err
	}
	frame, err := c.Receive(ctx)
	if err != nil {
		return err
	}
	receipt, ok := frame.Payload.(protocol.RestartStatus)
	if !ok || !receipt.Owed || frame.Error != nil {
		return errors.New("update_drain_failed")
	}
	return nil
}
func withStoppedCore(root string, fn func() error) error {
	s, err := ctlfiles.Open(root)
	if err != nil {
		return err
	}
	defer s.Close()
	if err = s.Acquire(); err != nil {
		return err
	}
	return fn()
}
func waitCoreStopped(ctx context.Context, root string, observer *ctlfiles.Store) error {
	return pollUpdate(ctx, func() (bool, error) {
		err := withStoppedCore(root, func() error {
			r, err := observer.ReadRestart()
			if err != nil {
				return err
			}
			if r == nil || r.CommittedEpoch == "" {
				return errors.New("update_drain_failed")
			}
			return nil
		})
		if errors.Is(err, ctlfiles.ErrLocked) {
			return false, nil
		}
		return err == nil, err
	})
}
func pollUpdate(ctx context.Context, check func() (bool, error)) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		done, err := check()
		if done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func waitUpdateClient(ctx context.Context, socket string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return pollUpdate(ctx, func() (bool, error) {
		c, err := clientapi.DialWithCapabilities(ctx, socket, "update", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
		if err != nil {
			if errors.Is(err, clientapi.ErrUnavailable) {
				return false, nil
			}
			return false, err
		}
		defer c.Close()
		if _, err = c.Input(ctx, protocol.Input{}); err != nil {
			return false, err
		}
		frame, err := c.Receive(ctx)
		if err != nil {
			return false, err
		}
		if frame.Error == nil {
			return false, errors.New("update_invalid_readiness")
		}
		if frame.Error.Code == protocol.ErrorUnavailable {
			return false, nil
		}
		return frame.Error.Code == protocol.ErrorMalformed, nil
	})
}
func rollbackUpdate(ctx context.Context, r ctlfiles.UpdateRecord) error {
	if r.ReachedReady || r.Generation == "" {
		return errors.New("update_rollback_forbidden")
	}
	m, err := backup.Verify(ctx, r.Generation)
	if err != nil {
		return err
	}
	// Verify the kept pair before making any filesystem changes. On a resumed
	// rollback, the old manifest may already be back in the active directory.
	path := filepath.Join(r.Previous, "bin/manifest.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		path = filepath.Join(r.Root, "system/bin/manifest.json")
	}
	old, err := manifest.Read(path)
	if err != nil {
		return err
	}
	if m.Artifacts["lina"] != old.Version+"@"+old.Commit {
		return errors.New("update_incompatible_backup")
	}
	if err = withStoppedCore(r.Root, func() error { return update.RestoreComposition(r) }); err != nil {
		return err
	}
	old, err = update.VerifyInstalled(ctx, r.Root)
	if err != nil {
		return err
	}
	found := false
	for _, b := range old.Binaries {
		if b.Name == "lina" && b.SHA256 == r.PreviousDigest {
			found = true
		}
	}
	if !found {
		return errors.New("update_incompatible_backup")
	}
	host := install.NewProcessHost()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		host.Close(cleanup)
	}()
	// The kept binary owns its own schema catalog. Never let a newer updater
	// migrate the snapshot while attempting to restore an older composition.
	_, err = host.RunWithContext(ctx, filepath.Join(r.Root, "system/bin/lina"), "restore", "--state-root", r.Root, "--generation", r.Generation)
	return err
}
func resumeUpdate(ctx context.Context, s *ctlfiles.Store, r ctlfiles.UpdateRecord, service func(context.Context, string) error) (ctlfiles.UpdateRecord, error) {
	if err := withStoppedCore(r.Root, func() error { return nil }); err != nil {
		return r, err
	}
	action := recover.UpdateAction(r)
	if action == "hold" {
		r.Phase = "held"
		if r.Cause == "" {
			r.Cause = "update_interrupted"
		}
		return r, errors.Join(fmt.Errorf("update_held: %s", r.Cause), s.WriteUpdate(r))
	}
	if action == "rollback" {
		r.Phase = "rollback"
		if err := s.WriteUpdate(r); err != nil {
			return r, err
		}
		if err := rollbackUpdate(ctx, r); err != nil {
			r.Phase = "held"
			r.Cause = "update_rollback_failed"
			return r, errors.Join(err, s.WriteUpdate(r))
		}
	}
	if action != "resume" {
		r.Phase = "rolled_back"
		r.Cause = "update_interrupted"
		if err := s.WriteUpdate(r); err != nil {
			return r, err
		}
	}
	if err := service(ctx, "start"); err != nil {
		return r, err
	}
	return r, waitUpdateClient(ctx, r.Socket)
}
