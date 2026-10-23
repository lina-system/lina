// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func restoreTarget(t *testing.T, root string, epoch string) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"main.json": `{"main_id":"main-test"}`, "epoch": epoch, "lifecycle.json": `{"schema":1}`} {
		if err := ctl.Write(name, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestoreWholeGenerationToSameAndDifferentRoot(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same", false: "other"}[same], func(t *testing.T) {
			cfg, assets := fixture(t)
			a := material(t, assets)
			if err := os.MkdirAll(filepath.Join(cfg.Root, "workspace"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Root, "workspace/note"), []byte("captured"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Canon.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), `INSERT INTO authority_effects VALUES('pending','fingerprint','{"Key":"pending","State":"none","Outward":true,"Epoch":1}',1);
    INSERT INTO authority_targets VALUES('target','rev','agent','pending');
    INSERT INTO accounts VALUES('account','{"ID":"account","Status":"ok"}');`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			m, err := Create(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			assets.Close()
			cfg.Canon.Close(context.Background())
			cfg.Controls.(*ctlfiles.Store).Close()
			target := cfg.Root
			if !same {
				target = filepath.Join(t.TempDir(), "other")
			}
			restoreTarget(t, target, "45")
			if err := os.MkdirAll(filepath.Join(target, "workspace"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "workspace/extra"), []byte("newer"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(target, "system/core/epoch"))
			if err != nil {
				t.Fatal(err)
			}
			options := RestoreConfig{Root: target, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Migrations: canon.Migrations(), Artifacts: cfg.Artifacts}
			result, err := Restore(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			if result.GenerationID != m.GenerationID {
				t.Fatal("wrong generation")
			}
			for _, name := range []string{"workspace/note", a.Path} {
				if _, err := os.Stat(filepath.Join(target, name)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(target, "workspace/extra")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("mixed generations")
			}
			after, _ := os.ReadFile(filepath.Join(target, "system/core/epoch"))
			if string(after) != string(before) {
				t.Fatal("control epoch replaced")
			}
			_, epoch, err := canon.Inspect(t.Context(), filepath.Join(target, "canon/canon.sqlite"))
			if err != nil || epoch != 45 {
				t.Fatalf("epoch %d %v", epoch, err)
			}
			reader, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(target, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			var effect, account, claim string
			var running bool
			err = reader.QueryRowContext(t.Context(), "SELECT json_extract(record,'$.State'),running FROM authority_effects WHERE effect_key='pending'").Scan(&effect, &running)
			if err == nil {
				err = reader.QueryRowContext(t.Context(), "SELECT json_extract(record,'$.Status') FROM accounts WHERE id='account'").Scan(&account)
			}
			if err == nil {
				err = reader.QueryRowContext(t.Context(), "SELECT active_key FROM authority_targets WHERE target='target'").Scan(&claim)
			}
			reader.Close()
			if err != nil || effect != "unknown" || running || account != "needs_sign_in" || claim != "" {
				t.Fatalf("restored authority: %s %v %s %s %v", effect, running, account, claim, err)
			}
			if _, err := Verify(t.Context(), options.Generation); err != nil {
				t.Fatalf("generation mutated: %v", err)
			}
			// The public entry point also resumes a publication interrupted after canon.
			options.boundary = func(at string) error {
				if at == "installed:canon" {
					return errors.New("interrupted")
				}
				return nil
			}
			if _, err := Restore(t.Context(), options); err == nil {
				t.Fatal("missing interruption")
			}
			options.boundary = nil
			if _, err := Restore(t.Context(), options); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestoreRejectsCorruptionAndIncompatibleReleaseWithoutChangingTarget(t *testing.T) {
	cfg, assets := fixture(t)
	a := material(t, assets)
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	assets.Close()
	cfg.Canon.Close(context.Background())
	cfg.Controls.(*ctlfiles.Store).Close()
	target := filepath.Join(t.TempDir(), "target")
	restoreTarget(t, target, "50")
	options := RestoreConfig{Root: target, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Migrations: canon.Migrations(), Artifacts: map[string]string{"lina": "incompatible"}}
	if _, err := Restore(t.Context(), options); err == nil {
		t.Fatal("incompatible artifacts restored")
	}
	options.Artifacts = cfg.Artifacts
	if err := os.WriteFile(filepath.Join(options.Generation, a.Path), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(t.Context(), options); err == nil {
		t.Fatal("corrupt generation restored")
	}
	if _, err := os.Stat(filepath.Join(target, "canon")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed validation published canon")
	}
	var identity map[string]string
	raw, _ := os.ReadFile(filepath.Join(target, "system/core/main.json"))
	if json.Unmarshal(raw, &identity) != nil || identity["main_id"] != "main-test" {
		t.Fatal("identity changed")
	}
}

func TestRestoreZeroExposureAfterLosingSourceCanon(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same", false: "other"}[same], func(t *testing.T) {
			cfg, assetStore := fixture(t)
			const body = "synthetic violet preference"
			_, err := ledger.New(cfg.Canon, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: "conversation", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
			if err != nil {
				t.Fatal(err)
			}
			flow := memoryexec.NewMemory(cfg.Canon, nil, nil)
			receipt, err := flow.Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: body, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}}})
			if err != nil {
				t.Fatal(err)
			}
			m, err := Create(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			removal, err := retract.Open(t.Context(), retract.Config{Canon: cfg.Canon, Root: cfg.Root})
			if err != nil {
				t.Fatal(err)
			}
			if err := removal.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
				t.Fatal(err)
			}
			removal.Close()
			assetStore.Close()
			cfg.Canon.Close(context.Background())
			cfg.Controls.(*ctlfiles.Store).Close()
			archive := t.TempDir()
			source, err := os.OpenRoot(cfg.Root)
			if err != nil {
				t.Fatal(err)
			}
			targetArchive, err := os.OpenRoot(archive)
			if err != nil {
				t.Fatal(err)
			}
			files, dirs := []File{}, []string{}
			err = copyTree(t.Context(), source, targetArchive, "backups", func(string) error { return nil }, &files, &dirs)
			source.Close()
			targetArchive.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(cfg.Root); err != nil {
				t.Fatal(err)
			}
			target := cfg.Root
			if !same {
				target = filepath.Join(t.TempDir(), "second")
			}
			restoreTarget(t, target, "91")
			if _, err := Restore(t.Context(), RestoreConfig{Root: target, Generation: filepath.Join(archive, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations()}); err != nil {
				t.Fatal(err)
			}
			ctl, err := ctlfiles.Open(target)
			if err != nil {
				t.Fatal(err)
			}
			defer ctl.Close()
			if err := ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(target, "canon/canon.sqlite"), Epoch: 91, Controls: ctl, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(context.Background())
			var count int
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_visible_memory WHERE id=?", receipt.ID).Scan(&count)
			}); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("first query by id resurrected forgotten memory")
			}
			hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Person: "person", Agent: "agent"}, memory.Query{Text: "violet", At: 100})
			if err != nil || len(hits) != 0 {
				t.Fatalf("recall: %+v %v", hits, err)
			}
			events, err := ledger.New(db, nil).Search(t.Context(), ledger.SearchQuery{PersonID: "person", AgentID: "agent", Text: "violet"})
			if err != nil || len(events) != 0 {
				t.Fatalf("ledger fallback: %+v %v", events, err)
			}
			if r, err := memoryexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: body, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}}}); err == nil && r.Remembered() {
				t.Fatal("forgotten evidence extracted after restore")
			}
		})
	}
}

func TestRestoreRejectsLinkedTargetBeforePublication(t *testing.T) {
	cfg, store := fixture(t)
	material(t, store)
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	cfg.Canon.Close(context.Background())
	cfg.Controls.(*ctlfiles.Store).Close()
	target := filepath.Join(t.TempDir(), "target")
	restoreTarget(t, target, "80")
	if err := os.Symlink(t.TempDir(), filepath.Join(target, "materials")); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(t.Context(), RestoreConfig{Root: target, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations()}); err == nil {
		t.Fatal("linked target accepted")
	}
	if err := CheckRestore(target); err != nil {
		t.Fatalf("validation began partial publication: %v", err)
	}
}

func TestRestoreResumeAppliesRemovalsRecordedDuringInterruption(t *testing.T) {
	cfg, store := fixture(t)
	_, err := ledger.New(cfg.Canon, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: "conversation", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "synthetic violet"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := memoryexec.NewMemory(cfg.Canon, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: "synthetic violet", Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source", Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	cfg.Canon.Close(context.Background())
	cfg.Controls.(*ctlfiles.Store).Close()
	target := filepath.Join(t.TempDir(), "target")
	restoreTarget(t, target, "80")
	options := RestoreConfig{Root: target, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Artifacts: cfg.Artifacts, Migrations: canon.Migrations(), boundary: func(at string) error {
		if at == "installed:canon" {
			return errors.New("interrupted")
		}
		return nil
	}}
	if _, err := Restore(t.Context(), options); err == nil {
		t.Fatal("missing interruption")
	}
	live, assets := fixtureAt(t, cfg.Root)
	removals, err := retract.Open(t.Context(), retract.Config{Canon: live.Canon, Root: live.Root})
	if err != nil {
		t.Fatal(err)
	}
	if err := removals.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
	options.boundary = nil
	if _, err := Restore(t.Context(), options); !errors.Is(err, ctlfiles.ErrLocked) {
		t.Errorf("resume ignored running source: %v", err)
	}
	removals.Close()
	assets.Close()
	live.Canon.Close(context.Background())
	live.Controls.(*ctlfiles.Store).Close()
	if _, err := Restore(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	db, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(target, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_visible_memory WHERE id=?", receipt.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("resumed restore exposed a newly forgotten item")
	}
}
