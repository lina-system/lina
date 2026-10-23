// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
)

func TestACPOnlyInstallationStartsDispatch(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	dir := t.TempDir()
	for _, name := range []string{"gemini", "git", "gh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	r.ctx = t.Context()
	r.route = &modelpath.Config{}
	r.activities = lifecycle.NewRegistry(nil)
	var err error
	r.personaAuthority, err = authority.Open(t.Context(), r.db, authority.Config{Epoch: r.epoch, Resolve: r.resolvePersonaGrant})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.startWorkerDispatch(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if r.workerDispatch != nil {
			r.workerDispatch.Close(context.Background())
		}
		if r.workerGitHub != nil {
			r.workerGitHub.Close(context.Background())
		}
	})
	if r.workerDispatch == nil || !r.workerDispatch.WorkerAvailable("gemini") {
		t.Fatal("ACP-only installation did not start dispatch")
	}
}
