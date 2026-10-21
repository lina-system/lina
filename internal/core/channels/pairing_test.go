// SPDX-License-Identifier: Apache-2.0
package channels_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func store(t *testing.T) (*channels.Store, *canon.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	migrations := canon.Migrations()
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return channels.New(db), db
}
func TestPairingSingleUseExpiryIdentityAndOwnership(t *testing.T) {
	s, db := store(t)
	ctx := t.Context()
	b := channels.Bot{Platform: "synthetic", Account: "bot"}
	now := time.Unix(1700000000, 0)
	if err := s.Connect(ctx, b, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Connect(ctx, b, "other"); !errors.Is(err, channels.ErrDenied) {
		t.Fatal(err)
	}
	if _, err := s.PairingCode(ctx, b, "other", now); !errors.Is(err, channels.ErrDenied) {
		t.Fatal(err)
	}
	code, err := s.PairingCode(ctx, b, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	var hash []byte
	if err = db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT pairing_hash FROM channel_connections").Scan(&hash)
	}); err != nil {
		t.Fatal(err)
	}
	if string(hash) == code || len(hash) != 32 {
		t.Fatal("plaintext or missing pairing hash")
	}
	if err = s.Pair(ctx, b, code, "username", "chat", now); !errors.Is(err, channels.ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.Pair(ctx, b, code, "123", "chat", now.Add(channels.PairingLifetime)); !errors.Is(err, channels.ErrDenied) {
		t.Fatal(err)
	}
	newer, err := s.PairingCode(ctx, b, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if newer == code {
		t.Fatal("reused random code")
	}
	if err = s.Pair(ctx, b, code, "123", "chat", now); !errors.Is(err, channels.ErrDenied) {
		t.Fatal(err)
	}
	if err = s.Pair(ctx, b, newer, "123", "chat", now); err != nil {
		t.Fatal(err)
	}
	if err = s.Pair(ctx, b, newer, "456", "chat", now); !errors.Is(err, channels.ErrDenied) {
		t.Fatal(err)
	}
	newer, err = s.PairingCode(ctx, b, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Pair(ctx, b, newer, "456", "chat", now); !errors.Is(err, channels.ErrDenied) {
		t.Fatal("identity transferred", err)
	}
}
func TestAddressKeysDoNotCollide(t *testing.T) {
	a := channels.Address{Bot: channels.Bot{Platform: "p", Account: "b:c"}, Chat: "d"}
	b := channels.Address{Bot: channels.Bot{Platform: "p", Account: "b"}, Chat: "c:d"}
	if a.Key() == b.Key() || a.Bot.Key() == b.Bot.Key() {
		t.Fatal("ambiguous keys")
	}
}
