// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/thisisjun786/lina/internal/core/miniapps"
)

// A declared host that resolves to a private address is refused, even when one
// public address is in the answer: the fetcher never picks the public one.
func TestMiniAppFetchRefusesANonPublicAnswer(t *testing.T) {
	f := miniAppFetcher{lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "mixed.example.com" {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}}
	hosts := []string{"mixed.example.com", "ok.example.com"}
	if _, _, err := f.Fetch(context.Background(), hosts, "https://mixed.example.com/x"); err == nil {
		t.Fatal("a host with a private answer was fetched")
	}
	if _, err := f.resolve(context.Background(), "127.0.0.1"); err == nil {
		t.Fatal("a loopback address was accepted")
	}
	if ip, err := f.resolve(context.Background(), "ok.example.com"); err != nil || ip.String() != "93.184.216.34" {
		t.Fatalf("a public answer was refused: %v %v", ip, err)
	}
}

// A redirect to an undeclared host is refused before the connection is made.
func TestMiniAppFetchRefusesARedirectToAnUndeclaredHost(t *testing.T) {
	f := miniAppFetcher{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}}
	if _, _, err := f.Fetch(context.Background(), []string{"declared.example.com"}, "https://undeclared.example.com/x"); err == nil {
		t.Fatal("an undeclared host was fetched")
	}
	// The declared host itself passes the checks; only the dial fails, because
	// the answer is a documentation address with no server.
	if _, _, err := f.Fetch(context.Background(), []string{"declared.example.com"}, "https://declared.example.com/x"); errors.Is(err, miniapps.ErrForbidden) {
		t.Fatal("a declared host was refused before the dial")
	}
}
