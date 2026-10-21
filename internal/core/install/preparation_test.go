// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Exercise LN-58's real preparation flow on an installation made without an
// account. No parallel preparation implementation or client operation is added.
func TestFirstPreparationOffersSignInWithoutCallingProvider(t *testing.T) {
	cfg, host := fixture(t)
	if _, err := Run(t.Context(), cfg, host); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(cfg.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: turn.Schema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(cfg.StateRoot, "canon/canon.sqlite"), Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	input, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "first-input"}, Body: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	var shown []turn.PreparationFailure
	steps, calls := 0, 0
	preparation := &turn.Preparation{
		VerifyBuild: func(context.Context) error {
			steps++
			_, err := readBundle(filepath.Join(cfg.StateRoot, "system/bin"))
			return err
		},
		Account: func(context.Context) (provider.Credential, error) {
			steps++
			entries, err := os.ReadDir(filepath.Join(cfg.StateRoot, "secrets"))
			if err != nil {
				return provider.Credential{}, err
			}
			if len(entries) != 0 {
				t.Fatal("installer wrote credentials")
			}
			return provider.Credential{}, nil
		},
		Show: func(f turn.PreparationFailure) { shown = append(shown, f) },
	}
	s := turn.New(db)
	_, err = s.Run(t.Context(), turn.Options{ID: "first-turn", Conversation: "conversation", InboxID: input.ID, Preparation: preparation, Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		calls++
		return nil, errors.New("provider must not be called")
	}}})
	var failure *turn.PreparationFailure
	if !errors.As(err, &failure) || steps != 2 || calls != 0 || len(shown) != 1 || shown[0].Step != turn.AccountStep || !shown[0].SignIn || shown[0].Line == "" || strings.ContainsAny(shown[0].Line, "\r\n") {
		t.Fatalf("first preparation: err=%v steps=%d calls=%d shown=%+v", err, steps, calls, shown)
	}
	row, err := s.Read(t.Context(), "first-turn")
	if err != nil || row.Prepared || row.State != turn.Failed {
		t.Fatalf("failed preparation was not durable: %+v %v", row, err)
	}
}
