// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
)

type acceptanceWriteFault struct {
	*canon.Store
	failure error
}

func (db acceptanceWriteFault) Write(ctx context.Context, f func(*canon.Tx) error) error {
	return db.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		return db.failure
	})
}

func TestM1CanonFaultAdmissionAndDoctor(t *testing.T) {
	t.Parallel()

	binary := buildLina(t)
	for _, fault := range []struct {
		name    string
		failure error
		halted  bool
	}{
		{"disk_full", sqlite3.FULL, false}, {"write_fault", sqlite3.IOERR_WRITE, false}, {"corruption", sqlite3.CORRUPT, true},
	} {
		t.Run(fault.name, func(t *testing.T) {
			h := newHarness(t, binary)
			core := h.startCore()
			h.inputRefused(h.client(core))
			h.stopCore(core)
			db, close := installedAcceptanceStore(t, h)
			key := inbox.Key{Conversation: "fault-conversation", Source: "client", NativeID: "fault-input"}
			receipt, err := inbox.New(acceptanceWriteFault{db, fault.failure}).Admit(h.ctx, inbox.Input{Key: key, Body: []byte("synthetic")})
			want := fault.failure
			if fault.halted {
				want = canon.ErrWriteHalted
			}
			if !errors.Is(err, want) || receipt.ID != 0 || receipt.Inserted {
				t.Fatalf("failed write acknowledged: %+v %v", receipt, err)
			}
			if _, err := inbox.New(db).Get(h.ctx, key); !errors.Is(err, inbox.ErrNotFound) {
				t.Fatalf("failed write visible: %v", err)
			}
			receipt, err = inbox.New(db).Admit(h.ctx, inbox.Input{Key: key, Body: []byte("synthetic")})
			if fault.halted {
				if !errors.Is(err, canon.ErrWriteHalted) || receipt.ID != 0 {
					t.Fatalf("halt not latched: %+v %v", receipt, err)
				}
			} else if err != nil || receipt.ID == 0 {
				t.Fatalf("full disk did not recover: %+v %v", receipt, err)
			}
			close()
			doctor := h.command("doctor", "--state-root", h.root)
			doctor.start(t)
			exit := 0
			if fault.halted {
				exit = 78
			}
			doctor.wait(t, exit)
		})
	}
}
