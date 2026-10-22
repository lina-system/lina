// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestWebSetupStoresKeyWithoutEchoAndLoadsEachTurn(t *testing.T) {
	r := controlRuntime(t)
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	p, err := r.selectedWebProvider()
	if err != nil || p != nil {
		t.Fatal("fresh install has web provider", err)
	}
	key := "fc-synthetic-web-key"
	result, err := r.Web(t.Context(), owner, protocol.WebRequest{Operation: "set", Key: &key})
	if err != nil || !result.Configured {
		t.Fatal(result, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), key) {
		t.Fatal("key echoed")
	}
	if _, err := r.vault.Get(secrets.Key{Person: r.person, Service: "firecrawl", Name: "api_key"}); err != nil {
		t.Fatal("key not in secret store", err)
	}
	if p, err := r.selectedWebProvider(); err != nil || p == nil {
		t.Fatal("configured provider unavailable", err)
	}
	release := r.vault.BeginTurn()
	_, err = r.Web(t.Context(), owner, protocol.WebRequest{Operation: "clear"})
	release()
	if err == nil {
		t.Fatal("key changed during a turn")
	}
	if _, err := r.Web(t.Context(), owner, protocol.WebRequest{Operation: "clear"}); err != nil {
		t.Fatal(err)
	}
	if p, err := r.selectedWebProvider(); err != nil || p != nil {
		t.Fatal("cleared provider still offered", err)
	}
}

func TestWebSetupRejectsForeignAndMalformedRequests(t *testing.T) {
	r := controlRuntime(t)
	key := "fc-synthetic-key"
	for _, s := range []clientsrv.Session{{PersonID: "foreign", Epoch: r.epoch, CanDecide: true}, {PersonID: r.person, Epoch: r.epoch, CanDecide: false}, {PersonID: r.person, Epoch: r.epoch + 1, CanDecide: true}} {
		if _, err := r.Web(t.Context(), s, protocol.WebRequest{Operation: "set", Key: &key}); err == nil {
			t.Fatal("unauthorized key change")
		}
	}
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	for _, request := range []protocol.WebRequest{{Operation: "set"}, {Operation: "status", Key: &key}, {Operation: "clear", Key: &key}, {Operation: "unknown"}, {Operation: "set", Key: str("bad\nkey")}} {
		if _, err := r.Web(t.Context(), owner, request); err == nil {
			t.Fatal("malformed key request accepted")
		}
	}
}
