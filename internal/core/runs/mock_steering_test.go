// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestMockConsumesAcceptedSteeringBeforePublication(t *testing.T) {
	started, resume := make(chan struct{}), make(chan struct{})
	var calls, published atomic.Int32
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) { return nil, runs.ErrInvalid }, func(c *runs.Config) {
		c.DrawMock = func(ctx context.Context, r runs.Record, save func(json.RawMessage) error) (runs.Artifact, string, error) {
			if calls.Add(1) == 1 {
				if err := save(json.RawMessage(`{"request":"original","attempts":[{"fragment":"old candidate"}]}`)); err != nil {
					return runs.Artifact{}, "", err
				}
				close(started)
				select {
				case <-resume:
				case <-ctx.Done():
					return runs.Artifact{}, "", ctx.Err()
				}
			} else if !strings.Contains(r.Brief.Goal, "Make the controls smaller") {
				t.Error("accepted input was not included in the new request")
			}
			if err := save(json.RawMessage(`{"stop_reason":"all_passed"}`)); err != nil {
				return runs.Artifact{}, "", err
			}
			published.Add(1)
			return runs.Artifact{Body: r.Brief.Goal, Done: true}, "all_passed", nil
		}
	})
	b := brief()
	b.Result = "mock"
	id, _, err := s.Start(t.Context(), origin(), b)
	if err != nil {
		t.Fatal(err)
	}
	release(t, s, id)
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal("draw never started")
	}
	if err = s.FollowOnce(t.Context(), id, "new-input", "Make the controls smaller"); err != nil {
		t.Fatal(err)
	}
	close(resume)
	record := wait(t, s, id)
	if calls.Load() != 2 || published.Load() != 1 || len(record.FollowUps) != 0 || !strings.Contains(record.Artifact.Body, "Make the controls smaller") {
		t.Fatalf("steering lost: calls=%d publications=%d pending=%v body=%q", calls.Load(), published.Load(), record.FollowUps, record.Artifact.Body)
	}
}
func TestMockRefusesNewSteeringAfterPublicationFence(t *testing.T) {
	checked, finish := make(chan struct{}), make(chan struct{})
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) { return nil, runs.ErrInvalid }, func(c *runs.Config) {
		c.DrawMock = func(ctx context.Context, _ runs.Record, save func(json.RawMessage) error) (runs.Artifact, string, error) {
			if err := save(json.RawMessage(`{"stop_reason":"all_passed"}`)); err != nil {
				return runs.Artifact{}, "", err
			}
			close(checked)
			select {
			case <-finish:
			case <-ctx.Done():
				return runs.Artifact{}, "", ctx.Err()
			}
			return runs.Artifact{Body: "checked", Done: true}, "all_passed", nil
		}
	})
	b := brief()
	b.Result = "mock"
	id, _, err := s.Start(t.Context(), origin(), b)
	if err != nil {
		t.Fatal(err)
	}
	release(t, s, id)
	<-checked
	err = s.FollowOnce(t.Context(), id, "late", "Late feedback")
	close(finish)
	if !errors.Is(err, runs.ErrState) {
		t.Fatalf("acknowledged feedback after publication fence: %v", err)
	}
	record := wait(t, s, id)
	if len(record.FollowUps) != 0 {
		t.Fatal("refused feedback persisted")
	}
}
