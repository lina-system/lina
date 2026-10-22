// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestCancelInterruptedRequiresRecoveryAndEvidence(t *testing.T) {
	f := setup(t)
	definition := spec(false)
	input, err := definition.Schema(f.call.Args)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := f.service.Begin(t.Context(), f.call, definition, input)
	if err != nil {
		t.Fatal(err)
	}
	recoveries := 0
	recognize := func(context.Context, *canon.Tx, authority.Effect) (tools.Result, bool, error) {
		recoveries++
		return tools.Result{State: tools.Cancelled, Revision: "r2", Body: json.RawMessage(`{"interrupted":true}`)}, true, nil
	}
	// Neither a live handler nor an unknown handler in this epoch may lose its
	// reservation. Unknown can mean a timeout whose handler is still executing.
	if err = f.service.CancelInterrupted(t.Context(), recognize); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Finish(t.Context(), ticket, tools.Result{State: tools.Unknown}); err != nil {
		t.Fatal(err)
	}
	if err = f.service.CancelInterrupted(t.Context(), recognize); err != nil || recoveries != 0 {
		t.Fatalf("cancelled current epoch: %d %v", recoveries, err)
	}
	if err = f.store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.cfg.Epoch++
	f.call.Auth.Epoch++
	f.store, err = canon.Open(t.Context(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.open(t)
	preserved := func() {
		t.Helper()
		effect, e := f.service.Effect(t.Context(), f.call.Key)
		if e != nil || effect.State != tools.Unknown {
			t.Fatalf("lost unknown: %+v %v", effect, e)
		}
		var active string
		e = f.store.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT active_key FROM authority_targets WHERE target='document'").Scan(&active)
		})
		if e != nil || active != f.call.Key {
			t.Fatalf("lost reservation: %q %v", active, e)
		}
	}
	if err = f.service.CancelInterrupted(t.Context(), func(context.Context, *canon.Tx, authority.Effect) (tools.Result, bool, error) {
		return tools.Result{}, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	preserved()
	failedRead := errors.New("ledger read failed")
	if err = f.service.CancelInterrupted(t.Context(), func(context.Context, *canon.Tx, authority.Effect) (tools.Result, bool, error) {
		return tools.Result{}, false, failedRead
	}); !errors.Is(err, failedRead) {
		t.Fatal(err)
	}
	preserved()
	if err = f.service.CancelInterrupted(t.Context(), func(context.Context, *canon.Tx, authority.Effect) (tools.Result, bool, error) {
		return tools.Result{State: tools.Applied, Revision: "r2", Body: json.RawMessage(`{}`)}, true, nil
	}); !errors.Is(err, tools.ErrInvalid) {
		t.Fatal(err)
	}
	preserved()
	if err = f.service.CancelInterrupted(t.Context(), recognize); err != nil {
		t.Fatal(err)
	}
	f.observedRevision = "r2"
	f.grantedRevision = "r2"
	replay, err := f.service.Begin(t.Context(), f.call, definition, input)
	if err != nil || !replay.Replay || replay.Result.State != tools.Cancelled {
		t.Fatalf("old invocation repeated: %+v %v", replay, err)
	}
	// A second recovery is a no-op, but a newly authorized call can use the new
	// ledger revision instead of the interrupted invocation's stale baseline.
	if err = f.service.CancelInterrupted(t.Context(), recognize); err != nil || recoveries != 1 {
		t.Fatalf("recovery replay: %d %v", recoveries, err)
	}
	f.call.Key = "new-intent"
	f.call.Auth.Revision = "r2"
	fresh, err := f.service.Begin(t.Context(), f.call, definition, input)
	if err != nil || fresh.Replay {
		t.Fatalf("fresh call refused: %+v %v", fresh, err)
	}
}

func TestCancelInterruptedDoesNotReleaseAnotherReservation(t *testing.T) {
	f := setup(t)
	definition := spec(false)
	input, err := definition.Schema(f.call.Args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Begin(t.Context(), f.call, definition, input); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.cfg.Epoch++
	f.call.Auth.Epoch++
	f.store, err = canon.Open(t.Context(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.open(t)
	if err = f.store.Write(t.Context(), func(tx *canon.Tx) error {
		_, e := tx.ExecContext(t.Context(), "UPDATE authority_targets SET active_key='different',revision='later' WHERE target='document'")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	err = f.service.CancelInterrupted(t.Context(), func(context.Context, *canon.Tx, authority.Effect) (tools.Result, bool, error) {
		return tools.Result{State: tools.Cancelled, Revision: "r2", Body: json.RawMessage(`{}`)}, true, nil
	})
	if !errors.Is(err, tools.ErrKeyConflict) {
		t.Fatal(err)
	}
	effect, err := f.service.Effect(t.Context(), f.call.Key)
	if err != nil || effect.State != tools.Unknown {
		t.Fatalf("non-atomic cancellation: %+v %v", effect, err)
	}
	var active, revision string
	err = f.store.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT active_key,revision FROM authority_targets WHERE target='document'").Scan(&active, &revision)
	})
	if err != nil || active != "different" || revision != "later" {
		t.Fatalf("foreign target changed: %s %s %v", active, revision, err)
	}
}
