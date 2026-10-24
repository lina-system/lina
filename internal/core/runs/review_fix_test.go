// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestRunPersistenceMasksCredentials(t *testing.T) {
	t.Parallel()
	credential := "sk-proj-" + strings.Repeat("Synthetic", 5)
	var round atomic.Int64
	var work string
	s, _, store := setupWith(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		b, _ := json.Marshal(req.Items)
		if strings.Contains(string(b), credential) {
			t.Error("credential reached provider context")
		}
		if round.Add(1) == 1 {
			return call("read-secret-shaped-data"), nil
		}
		return answer(runs.Artifact{Body: credential + "\n-----BEGIN PRIVATE KEY-----\nsynthetic-private-bytes\n-----END PRIVATE KEY-----", Done: true}), nil
	}, func(c *runs.Config) {
		work = c.WorkDir
		tool := c.Tools["read"]
		tool.Handler.Execute = func(_ context.Context, c provider.ToolCall) loop.Result {
			b, _ := json.Marshal(map[string]string{"password": "synthetic-password", "nested": `{"token":"nested-password"}`, "evidence": credential})
			return loop.Result{ID: c.ID, Code: loop.OK, Output: b}
		}
		c.Tools["read"] = tool
	})
	b := brief()
	b.Goal += " " + credential
	id, _, err := s.Start(t.Context(), origin(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Follow(t.Context(), id, "api_key="+credential); err != nil {
		t.Fatal(err)
	}
	release(t, s, id)
	r := wait(t, s, id)
	record, _ := json.Marshal(r)
	transcript, err := os.ReadFile(filepath.Join(work, "runs", id, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	material, err := store.Read(t.Context(), r.Material.ID, r.Material.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"record": record, "transcript": transcript, "material": material} {
		for _, secret := range []string{credential, "synthetic-password", "nested-password", "synthetic-private-bytes"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s retained a credential", name)
			}
		}
	}
}
func TestCompletedScratchCallIsNotReplayedAfterRetry(t *testing.T) {
	t.Parallel()
	var rounds, writes atomic.Int64
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		switch rounds.Add(1) {
		case 1, 3:
			return call("same-scratch-call"), nil
		case 2:
			return nil, errors.New("interrupted")
		}
		return answer(runs.Artifact{Body: "draft", Done: true}), nil
	}, func(c *runs.Config) {
		tool := c.Tools["read"]
		tool.Kind = runs.Scratch
		tool.Handler.Execute = func(_ context.Context, c provider.ToolCall) loop.Result {
			writes.Add(1)
			return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`"draft saved"`)}
		}
		c.Tools["read"] = tool
	})
	id := start(t, s)
	release(t, s, id)
	r := wait(t, s, id)
	if writes.Load() != 1 || r.Outcome != runs.Complete {
		t.Fatalf("scratch writes=%d outcome=%s", writes.Load(), r.Outcome)
	}
}

// The adapter fails only the material handover; canon remains writable.
type flakyMaterials struct {
	*assets.Store
	fail atomic.Bool
}

func (m *flakyMaterials) SaveRunResult(ctx context.Context, id string, b []byte, a assets.Actor) (assets.Asset, error) {
	if m.fail.CompareAndSwap(true, false) {
		return assets.Asset{}, errors.New("temporary material failure")
	}
	return m.Store.SaveRunResult(ctx, id, b, a)
}

func TestLandingCanRetryWithoutCallingModelAgain(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		calls.Add(1)
		return answer(runs.Artifact{Body: "completed report", Done: true}), nil
	}, func(c *runs.Config) {
		flaky := &flakyMaterials{Store: c.Assets.(*assets.Store)}
		flaky.fail.Store(true)
		c.Assets = flaky
	})
	id := start(t, s)
	release(t, s, id)
	if err := s.Wait(t.Context(), id); err == nil {
		t.Fatal("injected landing failure was not observed")
	}
	release(t, s, id)
	r := wait(t, s, id)
	if r.Outcome != runs.Complete || calls.Load() != 1 {
		t.Fatalf("landing=%s model calls=%d", r.Outcome, calls.Load())
	}
}

type closeActionStream struct {
	provider.Stream
	closeAction func()
}

func (s *closeActionStream) Close() error { s.closeAction(); return s.Stream.Close() }
func TestFinalDrainSteeringIsConsumedExactlyOnce(t *testing.T) {
	t.Parallel()
	var service *runs.Service
	var id string
	var calls atomic.Int64
	service, _, _ = setup(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		if calls.Add(1) == 1 {
			return &closeActionStream{Stream: answer(runs.Artifact{Body: "before follow-up", Done: true}), closeAction: func() {
				if err := service.Follow(t.Context(), id, "also inspect appendix B"); err != nil {
					t.Error(err)
				}
			}}, nil
		}
		b, _ := json.Marshal(req.Items)
		if strings.Count(string(b), "also inspect appendix B") != 1 {
			t.Error("final-drain steering was lost or duplicated")
		}
		return answer(runs.Artifact{Body: "appendix inspected", Done: true}), nil
	})
	id = start(t, service)
	release(t, service, id)
	r := wait(t, service, id)
	if r.Outcome != runs.Complete || calls.Load() != 2 || len(r.FollowUps) != 0 {
		t.Fatalf("outcome=%s calls=%d pending=%d", r.Outcome, calls.Load(), len(r.FollowUps))
	}
}

func TestInterruptedScratchIntentIsNotReexecuted(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	var writes atomic.Int64
	var config runs.Config
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return call("interrupted-scratch"), nil
	}, func(c *runs.Config) {
		tool := c.Tools["read"]
		tool.Kind = runs.Scratch
		tool.Handler.Execute = func(ctx context.Context, c provider.ToolCall) loop.Result {
			writes.Add(1)
			close(entered)
			<-ctx.Done()
			return loop.Result{ID: c.ID, Code: loop.OK}
		}
		c.Tools["read"] = tool
		config = *c
	})
	id := start(t, s)
	release(t, s, id)
	receive(t, entered)
	// Shutdown while the handler has changed scratch but its result is uncommitted.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	config.Epoch++
	config.Stream = func(_ context.Context, req provider.Request) (provider.Stream, error) {
		b, _ := json.Marshal(req.Items)
		if !strings.Contains(string(b), "unknown") {
			t.Error("uncertain intent absent from recovery context")
		}
		return answer(runs.Artifact{Body: "retained draft", Done: true}), nil
	}
	resumed, err := runs.New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resumed.Close() })
	if err = resumed.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	release(t, resumed, id)
	r := wait(t, resumed, id)
	if writes.Load() != 1 || r.Outcome != runs.Partial || !r.Uncertain {
		t.Fatalf("writes=%d outcome=%s uncertain=%v", writes.Load(), r.Outcome, r.Uncertain)
	}
}
