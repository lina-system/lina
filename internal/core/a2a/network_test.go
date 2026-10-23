// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"net"
	"testing"
)

func TestDestinationBoundary(t *testing.T) {
	for _, raw := range []string{"http://example.com/rpc", "https://user:pass@example.com/rpc", "https://169.254.169.254/latest", "https://[fd00:ec2::254]/latest", "https://127.0.0.1/rpc", "https://[::1]/rpc", "https://example.com/rpc#token"} {
		t.Run(raw, func(t *testing.T) {
			p := Network{Resolve: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil }}
			if err := p.Check(context.Background(), raw, ""); err == nil {
				t.Fatal("forbidden destination admitted")
			}
		})
	}
	p := Network{Resolve: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.1.2.3")}, nil }}
	if p.Check(t.Context(), "https://peer.test/rpc", "") == nil {
		t.Fatal("private DNS target admitted without owner exception")
	}
	if err := p.Check(t.Context(), "https://peer.test/rpc", "peer.test"); err != nil {
		t.Fatal(err)
	}
	if p.Check(t.Context(), "https://other.test/rpc", "peer.test") == nil {
		t.Fatal("private exception widened")
	}
	if p.Check(t.Context(), "https://169.254.169.254/", "169.254.169.254") == nil {
		t.Fatal("metadata exception admitted")
	}
}
