// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func restoreDB(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 9, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func TestRestoreInvalidatesAuthority(t *testing.T) {
	db := restoreDB(t)
	states := []tools.State{tools.None, tools.Unknown, tools.Applied, tools.Failed, tools.Cancelled, tools.Refused}
	original := map[tools.State]string{}
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, state := range states {
			raw, _ := json.Marshal(authority.Effect{Key: string(state), State: state, Epoch: 9, Outward: true, Write: true, Result: tools.Result{State: state}})
			original[state] = string(raw)
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO authority_effects VALUES(?,?,?,?)", string(state), "fingerprint", raw, state == tools.None); err != nil {
				return err
			}
		}
		pending, _ := json.Marshal(authority.Approval{ID: "pending", State: authority.Approved, Epoch: 9})
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals VALUES('pending','effect','fingerprint',?)", pending); err != nil {
			return err
		}
		standing, _ := json.Marshal(authority.StandingGrant{ID: "standing", Person: "person"})
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO authority_standing VALUES('standing','person',?)", standing); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO authority_targets VALUES('target','revision','agent','none')"); err != nil {
			return err
		}
		a, _ := json.Marshal(accounts.Account{ID: "account", Provider: provider.ChatGPT, Person: "person", Label: "test", Auth: provider.APIKey, Status: accounts.OK})
		_, err := tx.ExecContext(t.Context(), "INSERT INTO accounts VALUES('account',?)", a)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := invalidateAuthority(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, state := range states {
			var raw []byte
			var running bool
			if err := tx.QueryRowContext(t.Context(), "SELECT record,running FROM authority_effects WHERE effect_key=?", string(state)).Scan(&raw, &running); err != nil {
				return err
			}
			var e authority.Effect
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			if running {
				t.Error("restored running lease")
			}
			if state == tools.None {
				if e.State != tools.Unknown || e.Result.State != tools.Unknown {
					t.Errorf("live effect: %+v", e)
				}
			} else if string(raw) != original[state] {
				t.Errorf("changed terminal receipt: %s", state)
			}
		}
		var claim string
		if err := tx.QueryRowContext(t.Context(), "SELECT active_key FROM authority_targets").Scan(&claim); err != nil {
			return err
		}
		if claim != "" {
			t.Error("restored claim")
		}
		var raw []byte
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM authority_approvals").Scan(&raw); err != nil {
			return err
		}
		var approval authority.Approval
		if err := json.Unmarshal(raw, &approval); err != nil {
			return err
		}
		if approval.State != authority.Stopped {
			t.Errorf("restored approval: %+v", approval)
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM authority_standing").Scan(&raw); err != nil {
			return err
		}
		var standing authority.StandingGrant
		if err := json.Unmarshal(raw, &standing); err != nil {
			return err
		}
		if !standing.Revoked {
			t.Error("restored standing authority")
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT record FROM accounts").Scan(&raw); err != nil {
			return err
		}
		var a accounts.Account
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if a.Status != accounts.NeedsSignIn {
			t.Errorf("restored account: %+v", a)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRestoreAuthorityFailureIsAtomic(t *testing.T) {
	db := restoreDB(t)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO authority_targets VALUES('target','revision','agent','claim')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO accounts VALUES('account','null')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := invalidateAuthority(t.Context(), db); err == nil {
		t.Fatal("malformed account restored")
	}
	var claim string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT active_key FROM authority_targets").Scan(&claim)
	}); err != nil {
		t.Fatal(err)
	}
	if claim != "claim" {
		t.Fatal("partly invalidated failed restore")
	}
}

func TestRestoreInvalidatesMergePermissionButKeepsUncertainReservation(t *testing.T) {
	db := restoreDB(t)
	service := authority.NewMerges(db, 9)
	for _, id := range []string{"ready", "unknown"} {
		b := authority.MergeBinding{ID: id, Person: "person", Agent: "agent", Repository: "o/r", Base: id, Number: 1, Clone: "/clone", Protected: map[string]string{"refs/heads/" + id: "base"}}
		if err := service.Bind(t.Context(), b); err != nil {
			t.Fatal(err)
		}
		if err := service.Accept(t.Context(), id, "person", "head"); err != nil {
			t.Fatal(err)
		}
		if id == "unknown" {
			if err := service.Begin(t.Context(), id, "person", "head", b.Protected); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := invalidateAuthority(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	ready, err := service.Get(t.Context(), "ready", "person")
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != "restored" {
		t.Fatalf("restored permission: %+v", ready)
	}

	// A refused request or unsuccessful observation must not undo invalidation.
	for _, state := range []string{"blocked", "unknown", "queued"} {
		if err = service.Record(t.Context(), "ready", "person", state, json.RawMessage(`{"State":"blocked"}`)); err != nil {
			t.Fatal(err)
		}
		after, e := service.Get(t.Context(), "ready", "person")
		if e != nil || after.State != "restored" {
			t.Fatalf("%s revived restored binding: %+v %v", state, after, e)
		}
	}
	if err = service.Accept(t.Context(), "ready", "person", "head"); err == nil {
		t.Fatal("restored permission accepted")
	}
	unknown, err := service.Get(t.Context(), "unknown", "person")
	if err != nil || unknown.State != "unknown" {
		t.Fatalf("reservation: %+v %v", unknown, err)
	}
}
