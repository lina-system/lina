// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/update"
)

func TestM2UpdateClaudeCredential(t *testing.T) {
	real := buildLina(t)
	node := buildCommand(t, "lina-node")
	worker := buildCommand(t, "moirai-worker")
	runtime, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	program, err := filepath.Abs("../m2/update-claude-runtime.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), real, node)
	addUpdateWorker(t, bundle)
	candidate := makeInstallBundle(t, t.TempDir(), real, node)
	marker := filepath.Join(t.TempDir(), "worker-ready")
	// Observe the smoke worker's actual environment, then run its real preflight.
	script := []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\n" +
		"test -z \"${CLAUDE_CODE_OAUTH_TOKEN+x}\" || exit 91\n" +
		"'" + worker + "' \"$@\" || exit $?\n" +
		"printf ready > '" + marker + "'\n")
	if err := os.WriteFile(filepath.Join(candidate, "moirai-worker"), script, 0700); err != nil {
		t.Fatal(err)
	}
	alterUpdateManifest(t, candidate, func(m map[string]any) {
		m["version"] = "0.0.1-alpha.20261022.1"
		m["binaries"] = append(m["binaries"].([]any), map[string]any{"name": "moirai-worker", "size": len(script), "sha256": fmt.Sprintf("%x", sha256.Sum256(script))})
	})
	archive, digest := packUpdate(t, candidate)
	for _, configured := range []bool{true, false} {
		name := "configured-token"
		if !configured {
			name = "missing-token"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, real)
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			h.ctx = ctx
			h, _ = installConversationBundle(t, h, bundle, "")
			// Every command gets the harness's private HOME; no cached login or
			// developer credential can satisfy this runtime's authentication.
			env := []string{"LINA_CLAUDE_NODE=" + runtime, "LINA_CLAUDE_ADAPTER=" + program, "LINA_CLAUDE_MODEL=fixture-model"}
			const token = "synthetic-update-claude-token"
			if configured {
				env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
			}
			start := func(bool) *process {
				core := h.command("core", "--state-root", h.root, "--socket", h.socket)
				core.command.Env = append(core.command.Env, env...)
				core.start(t)
				h.connect(core).Close()
				waitCoreReady(t, h)
				return core
			}
			core := start(true)
			tools, manager, current := updateManager(t, h, start, core)
			prepare := h.command("update", "--prepare", "--state-root", h.root, "--archive", archive, "--sha256", digest)
			prepare.start(t)
			prepare.wait(t, 0)
			var prepared update.Prepared
			if err := json.Unmarshal(prepare.stdout.Bytes(), &prepared); err != nil {
				t.Fatal(err)
			}
			activate := h.command("update", "--activate", "--state-root", h.root, "--candidate", prepared.Directory)
			activate.command.Env = append(activate.command.Env, env...)
			activate.command.Env = append(activate.command.Env, "PATH="+tools, "LINA_UPDATE_MANAGER_SOCKET="+manager)
			activate.start(t)
			code := 78
			if configured {
				code = 0
			}
			// Activation spans drain, backup, swap, smoke and service resume.
			// As in the shared activation scenario, wait for the actual process
			// within this scenario's deadline before checking its exit status.
			select {
			case <-activate.done:
			case <-ctx.Done():
				activate.killAndWait()
				t.Fatalf("activation did not finish: %s", &activate.stderr)
			}
			activate.wait(t, code)
			var receipt ctlfiles.UpdateRecord
			if err := json.Unmarshal(activate.stdout.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if !receipt.ReachedReady || receipt.Generation == "" {
				t.Fatal("update did not reach the conversation smoke")
			}
			if configured {
				if receipt.Phase != "complete" {
					t.Fatalf("update phase = %s", receipt.Phase)
				}
				if data, err := os.ReadFile(marker); err != nil || string(data) != "ready" {
					t.Fatal("credential-free worker preflight did not complete")
				}
				h.stopCore(current())
			} else if receipt.Phase != "held" || receipt.Cause == "" {
				t.Fatal("missing credential did not hold the update")
			}
			core.wait(t, 75)
			doctor := h.command("doctor", "--state-root", h.root)
			doctor.start(t)
			doctor.wait(t, 0)
			for _, p := range []*process{core, prepare, activate, doctor, current()} {
				if bytes.Contains(p.stdout.Bytes(), []byte(token)) || bytes.Contains(p.stderr.Bytes(), []byte(token)) {
					t.Fatal("credential reached command output or diagnostics")
				}
			}
			// Include receipts, logs, canon and backup snapshots; executable
			// artifacts are immutable inputs rather than runtime evidence.
			if err := filepath.WalkDir(h.root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 != 0 {
					return err
				}
				data, err := os.ReadFile(path)
				if err == nil && bytes.Contains(data, []byte(token)) {
					return fmt.Errorf("credential persisted in %s", entry.Name())
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
