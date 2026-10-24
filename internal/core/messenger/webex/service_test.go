// SPDX-License-Identifier: Apache-2.0

package webex_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func TestServiceBindsConsentAndStopsReaderBeforeDisconnect(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	started, cancelled := make(chan struct{}), make(chan struct{})
	rec := &recorder{}
	server := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rooms" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	defer server.Close()
	service := webex.NewService(t.Context(), messenger.Open(db, nil, nil), webex.Config{Person: person, Vault: vault, BaseURL: server.URL})
	defer service.Close()
	if err := service.Configure(t.Context(), clientID, clientSecret); err != nil {
		t.Fatal(err)
	}
	target, err := service.Begin(t.Context(), "connection", 8400)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(target)
	redirect := webex.Redirect{Code: "code", State: parsed.Query().Get("state")}
	if _, err := service.Complete(t.Context(), "other", redirect); err == nil {
		t.Fatal("another connection consumed consent")
	}
	if _, err := service.Begin(t.Context(), "other", 8400); err == nil {
		t.Fatal("another consent replaced pending state")
	}
	account, err := service.Complete(t.Context(), "connection", redirect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Complete(t.Context(), "connection", redirect); err == nil {
		t.Fatal("replayed consent accepted")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not start")
	}
	if err := service.Configure(t.Context(), "other", "other"); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatal("active integration replaced", err)
	}
	if err := service.Disconnect(t.Context(), account.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not stop")
	}
	if _, err := vault.GetTokens(webex.TokenKey(person)); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("tokens retained", err)
	}
	rows, err := service.Sources(t.Context())
	if err != nil || len(rows) != 1 || rows[0].State != messenger.StateDisconnected {
		t.Fatalf("source: %+v %v", rows, err)
	}
}
