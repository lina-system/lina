// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRunSettlesDeliveryFailures(t *testing.T) {
	for _, tc := range []struct {
		name, kind         string
		failures, attempts int
		state              string
	}{
		{"transient sensor", "sensor", 1, 2, "handled"},
		{"persistent sensor", "sensor", 2, 2, "unknown"},
		{"effect capable job", "job", 1, 1, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, _ := openTimers(t, t.TempDir(), 1)
				now := time.Now().UTC().Truncate(time.Minute)
				tm := timerFixture("failure", tc.kind)
				if _, err := s.Create(t.Context(), tm, now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var calls atomic.Int32
				go s.Run(ctx, "1", func(ctx context.Context, f Fire) error {
					n := calls.Add(1)
					if n <= int32(tc.failures) {
						return errors.New("temporary source failure")
					}
					return nil
				})
				time.Sleep(5 * time.Second) // Virtual time; wait for bounded recovery, not wall-clock luck.
				synctest.Wait()
				status, err := s.Get(t.Context(), tm.ID, tm.Person, tm.Agent, tm.Conversation)
				if err != nil {
					t.Fatal(err)
				}
				if calls.Load() != int32(tc.attempts) || len(status.Fires) != 1 || status.Fires[0].State != tc.state {
					t.Fatalf("calls=%d want=%d fires=%+v want=%s", calls.Load(), tc.attempts, status.Fires, tc.state)
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				status, err = s.Get(t.Context(), tm.ID, tm.Person, tm.Agent, tm.Conversation)
				if err != nil || calls.Load() != int32(tc.attempts+1) || len(status.Fires) != 2 || status.Fires[0].State != "handled" {
					t.Fatalf("next occurrence blocked: calls=%d fires=%+v err=%v", calls.Load(), status.Fires, err)
				}
			})
		})
	}
}

func TestRunRetriesAcknowledgementWithoutDelivery(t *testing.T) {
	for _, state := range []string{"handled", "unknown"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, db := openTimers(t, t.TempDir(), 1)
				exec := func(query string) {
					t.Helper()
					if err := db.Write(t.Context(), func(tx *canon.Tx) error { _, err := tx.ExecContext(t.Context(), query); return err }); err != nil {
						t.Fatal(err)
					}
				}
				exec(`CREATE TRIGGER fail_ack BEFORE UPDATE OF state ON timer_fire WHEN NEW.state IN ('handled','unknown') BEGIN SELECT RAISE(ABORT,'temporary ack failure'); END`)
				tm := timerFixture("ack", "job")
				if _, err := s.Create(t.Context(), tm, time.Now().Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var calls atomic.Int32
				go s.Run(ctx, "1", func(context.Context, Fire) error {
					calls.Add(1)
					if state == "unknown" {
						return errors.New("delivery failed")
					}
					return nil
				})
				time.Sleep(5 * time.Second)
				synctest.Wait()
				status, err := s.Get(t.Context(), tm.ID, tm.Person, tm.Agent, tm.Conversation)
				if err != nil || calls.Load() != 1 || len(status.Fires) != 1 || status.Fires[0].State != "claimed" {
					t.Fatalf("fault not active: calls=%d status=%+v err=%v", calls.Load(), status, err)
				}
				exec("DROP TRIGGER fail_ack")
				time.Sleep(10 * time.Second)
				synctest.Wait()
				status, err = s.Get(t.Context(), tm.ID, tm.Person, tm.Agent, tm.Conversation)
				if err != nil || calls.Load() != 1 || len(status.Fires) != 1 || status.Fires[0].State != state {
					t.Fatalf("ack not recovered: calls=%d status=%+v err=%v", calls.Load(), status, err)
				}
			})
		})
	}
}
