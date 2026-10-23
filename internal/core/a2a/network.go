// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrDestination = errors.New("a2a_forbidden_destination")

// Network validates both discovery URLs and the addresses actually dialed.
// An owner exception permits private/loopback addresses for exactly one host;
// link-local, unspecified, multicast and metadata addresses remain forbidden.
type Network struct {
	Resolve func(context.Context, string) ([]net.IP, error)
	// TLS is an in-process test seam. Production uses the system trust store.
	TLS *tls.Config
}

func parsed(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.HasSuffix(u.Hostname(), ".") {
		return nil, ErrDestination
	}
	return u, nil
}
func origin(raw string) string {
	u, err := parsed(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}
func (n Network) addresses(ctx context.Context, host, exception string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolve := n.Resolve
		if resolve == nil {
			resolve = func(ctx context.Context, h string) ([]net.IP, error) {
				return net.DefaultResolver.LookupIP(ctx, "ip", h)
			}
		}
		var err error
		ips, err = resolve(ctx, host)
		if err != nil {
			return nil, ErrDestination
		}
	}
	if len(ips) == 0 {
		return nil, ErrDestination
	}
	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.Equal(net.ParseIP("100.100.100.200")) || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) {
			return nil, ErrDestination
		}
		// CGNAT includes tailnet addresses and requires the same explicit exception.
		cgn := ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1] >= 64 && ip.To4()[1] <= 127
		if (ip.IsPrivate() || ip.IsLoopback() || cgn) && !strings.EqualFold(host, exception) {
			return nil, ErrDestination
		}
	}
	return ips, nil
}
func (n Network) Check(ctx context.Context, raw, exception string) error {
	u, err := parsed(raw)
	if err != nil {
		return err
	}
	_, err = n.addresses(ctx, u.Hostname(), exception)
	return err
}
func (n Network) client(allowed, exception string, guards ...func(context.Context) error) *http.Client {
	tr := &http.Transport{Proxy: nil, TLSClientConfig: n.TLS, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrDestination
		}
		u, err := parsed(allowed)
		if err != nil || !strings.EqualFold(host, u.Hostname()) {
			return nil, ErrDestination
		}
		ips, err := n.addresses(ctx, host, exception)
		if err != nil {
			return nil, err
		}
		// Dial the checked address, never resolve again in the dialer.
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	tr.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := tr.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		config := &tls.Config{}
		if n.TLS != nil {
			config = n.TLS.Clone()
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			conn.Close()
			return nil, err
		}
		config.ServerName = host
		secure := tls.Client(conn, config)
		if err = secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		// No pooled connection bypasses this final authority check. DNS and TLS can
		// take time; a revoke during either must precede the first HTTP body byte.
		for _, guard := range guards {
			if err = guard(ctx); err != nil {
				secure.Close()
				return nil, err
			}
		}
		return secure, nil
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrDestination }}
}
