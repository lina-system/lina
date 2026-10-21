// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func serve(t *testing.T, s *Service) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	root, err := os.MkdirTemp("", "jobs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	path := filepath.Join(root, "job.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server failed to stop")
		}
	})
	return path, cancel, done
}
func TestSocketPrivateAndProtocol(t *testing.T) {
	s, _, _ := fixture(t)
	path, _, _ := serve(t, s)
	for name, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: %v %v", name, info, err)
		}
	}
	if l, err := Listen(path); err == nil {
		l.Close()
		t.Fatal("replaced live socket")
	}
	client := jobapi.NewClient(path)
	r, err := client.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1})
	if err != nil {
		t.Fatal(err)
	}
	p := jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: r.Output}
	if _, err = client.Apply(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err = client.Report(t.Context(), "memory", jobapi.JobState{Epoch: 1, Watermark: 1, Status: "applied"}); err != nil {
		t.Fatal(err)
	}
	p.Epoch = 2
	if _, err = client.Apply(t.Context(), p); !errors.Is(err, jobapi.ErrEpoch) {
		t.Fatal(err)
	}
}
func TestServerCancellationJoinsActiveCallAndIdleConnection(t *testing.T) {
	s, _, _ := fixture(t)
	started := make(chan struct{})
	finished := make(chan struct{})
	d := s.cfg.Definitions["memory"]
	d.Model = func(ctx context.Context, _ Call) (Result, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return Result{}, ctx.Err()
	}
	s.cfg.Definitions["memory"] = d
	path, cancel, _ := serve(t, s)
	idle, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	result := make(chan error, 1)
	go func() {
		_, err := jobapi.NewClient(path).Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("call not started")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("call not canceled")
	}
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("client not released")
	}
}
