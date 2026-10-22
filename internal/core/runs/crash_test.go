// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func recoveredService(t *testing.T, root string, epoch uint64, stream loop.StreamFunc) *runs.Service {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	migrations := canon.Migrations()
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s, err := runs.New(runs.Config{Epoch: epoch, WorkDir: filepath.Join(root, "work"), DB: db, Assets: store, Stream: stream, Authorize: func(_ context.Context, o runs.Origin, _ runs.Brief) error {
		if o.Epoch != epoch {
			return runs.ErrScope
		}
		return nil
	}, Tools: map[string]runs.Tool{
		"read": {Kind: runs.Read, Declaration: json.RawMessage(`{"type":"function","name":"read"}`), Authorize: func(context.Context, runs.Record, provider.ToolCall) error { return nil }, Handler: loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
			return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`"saved evidence"`)}
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestProcessDeathResumesOnceAndThenLandsPartial(t *testing.T) {
	if root := os.Getenv("LINA_RUN_CRASH_ROOT"); root != "" {
		epoch, err := strconv.ParseUint(os.Getenv("LINA_RUN_CRASH_EPOCH"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		s := recoveredService(t, root, epoch, func(_ context.Context, req provider.Request) (provider.Stream, error) {
			calls++
			if epoch == 1 && calls == 1 {
				return call("read-before-crash"), nil
			}
			b, _ := json.Marshal(req.Items)
			if !strings.Contains(string(b), "saved evidence") {
				t.Fatal("checkpoint did not carry the completed read")
			}
			os.Exit(73) // process dies after checkpoint commit and before the next response
			return nil, nil
		})
		var id string
		if epoch == 1 {
			id = start(t, s)
			if err := os.WriteFile(filepath.Join(root, "run-id"), []byte(id), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			b, err := os.ReadFile(filepath.Join(root, "run-id"))
			if err != nil {
				t.Fatal(err)
			}
			id = string(b)
			if err = s.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		release(t, s, id)
		wait(t, s, id)
		t.Fatal("child did not exit")
	}
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, epoch := range []string{"1", "2"} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		child := exec.CommandContext(ctx, binary, "-test.run=^TestProcessDeathResumesOnceAndThenLandsPartial$")
		child.Env = []string{"LINA_RUN_CRASH_ROOT=" + root, "LINA_RUN_CRASH_EPOCH=" + epoch}
		output, err := child.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 73 {
			t.Fatalf("epoch %s: %v %s", epoch, err, output)
		}
	}
	s := recoveredService(t, root, 3, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("third model attempt")
		return nil, runs.ErrInvalid
	})
	b, err := os.ReadFile(filepath.Join(root, "run-id"))
	if err != nil {
		t.Fatal(err)
	}
	id := string(b)
	if err = s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(t.Context(), id)
	if err != nil || r.Outcome != runs.Partial || r.Retries != 1 || r.Origin.Epoch != 3 || len(r.Calls) != 1 {
		t.Fatalf("record=%+v error=%v", r, err)
	}
	transcript, err := os.ReadFile(filepath.Join(root, "work", "runs", id, "transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), "saved evidence") {
		t.Fatalf("transcript=%s error=%v", transcript, err)
	}
	release(t, s, id)
	if err := s.Acknowledge(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	notices, err := s.Notices(t.Context(), origin().Conversation)
	if err != nil || len(notices) != 0 {
		t.Fatalf("acknowledged notices=%+v error=%v", notices, err)
	}
}
