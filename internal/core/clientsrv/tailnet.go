// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"time"
)

// TailnetConfig opts into one exact TCP address. Socket names the trusted local
// tailscaled Unix socket; it is never a remote HTTP endpoint.
type TailnetConfig struct {
	Address string
	Socket  string
	// Listen is an in-process network seam for isolated acceptance tests. CLI
	// configuration never supplies it; production always uses net.ListenConfig.
	Listen func(context.Context, string, string) (net.Listener, error)
}

type tailnetStatus struct {
	BackendState string
	TailscaleIPs []netip.Addr
	Self         *struct {
		ID     string
		UserID uint64
	}
}
type tailnetWho struct {
	Node *struct {
		StableID  string
		User      uint64
		Addresses []netip.Prefix
		Tags      []string
	}
	UserProfile *struct{ ID uint64 }
}

// TailnetListener retains the identity authority alongside the bound listener.
// Only ListenTailnet can construct an authenticated remote listener.
type TailnetListener struct {
	net.Listener
	api     *tailnetAPIClient
	address netip.Addr
	self    string
	owner   uint64
}
type tailnetAPIClient struct {
	client    *http.Client
	transport *http.Transport
}

func isTailnetIP(ip netip.Addr) bool {
	return ip.Zone() == "" && (netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip))
}

func ListenTailnet(ctx context.Context, cfg TailnetConfig) (*TailnetListener, error) {
	address, err := netip.ParseAddrPort(cfg.Address)
	if err != nil || !isTailnetIP(address.Addr()) {
		return nil, errors.New("tailnet listener requires a literal Tailscale IP and port")
	}
	socket := cfg.Socket
	if socket == "" {
		socket = "/var/run/tailscale/tailscaled.sock"
	}
	if !filepath.IsAbs(socket) {
		return nil, errors.New("tailscaled socket must be an absolute local path")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	api := &tailnetAPIClient{transport: transport, client: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var status tailnetStatus
	if err := api.get(ctx, "/status?peers=false", &status); err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	if status.BackendState != "Running" || status.Self == nil || status.Self.ID == "" || status.Self.UserID == 0 || !slices.Contains(status.TailscaleIPs, address.Addr()) {
		transport.CloseIdleConnections()
		return nil, errors.New("tailnet address is not owned by the running Tailscale identity")
	}
	listen := cfg.Listen
	if listen == nil {
		listen = (&net.ListenConfig{}).Listen
	}
	listener, err := listen(ctx, "tcp", address.String())
	if err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("listen on tailnet: %w", err)
	}
	return &TailnetListener{Listener: listener, api: api, address: address.Addr(), self: status.Self.ID, owner: status.Self.UserID}, nil
}

func (l *TailnetListener) Close() error {
	err := l.Listener.Close()
	l.api.transport.CloseIdleConnections()
	return err
}

func (api *tailnetAPIClient) get(ctx context.Context, path string, value any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock/localapi/v0"+path, nil)
	if err != nil {
		return err
	}
	response, err := api.client.Do(req)
	if err != nil {
		return errors.New("Tailscale LocalAPI unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("Tailscale LocalAPI refused identity lookup")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("invalid Tailscale identity response")
	}
	if err := json.Unmarshal(data, value); err != nil {
		return errors.New("invalid Tailscale identity response")
	}
	return nil
}

func (l *TailnetListener) inspect(ctx context.Context, conn net.Conn) (peerIdentity, error) {
	remote, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	if err != nil || !isTailnetIP(remote.Addr()) {
		return peerIdentity{}, errors.New("client is not a tailnet peer")
	}
	// Revalidate the daemon identity and the peer for each decision. A local
	// process must use the Unix socket so it cannot evade process ancestry.
	authorized := func() bool {
		var status tailnetStatus
		if l.api.get(ctx, "/status?peers=false", &status) != nil || status.BackendState != "Running" || status.Self == nil || status.Self.ID != l.self || status.Self.UserID != l.owner || !slices.Contains(status.TailscaleIPs, l.address) || slices.Contains(status.TailscaleIPs, remote.Addr()) {
			return false
		}
		var who tailnetWho
		if l.api.get(ctx, "/whois?addr="+url.QueryEscape(remote.String()), &who) != nil || who.Node == nil || who.UserProfile == nil || who.Node.StableID == "" || who.Node.StableID == l.self || who.Node.User != l.owner || who.UserProfile.ID != l.owner || len(who.Node.Tags) != 0 {
			return false
		}
		for _, address := range who.Node.Addresses {
			if address.Addr() == remote.Addr() && address.Bits() == remote.Addr().BitLen() {
				return true
			}
		}
		return false
	}
	if !authorized() {
		return peerIdentity{}, errors.New("tailnet peer is not the owner on another device")
	}
	return peerIdentity{remoteDecider: authorized}, nil
}
