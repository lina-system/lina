// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/thisisjun786/lina/kit/rpc"
)

type bufferedClient struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedClient) Read(data []byte) (int, error) { return c.reader.Read(data) }

// Select framing only after the listener authenticates the physical peer. Both
// transports share the same address and session engine; HTTP is not a proxy to
// a privileged local socket. Preserve all bytes buffered during sniffing.
func serveClientTransport(ctx context.Context, conn net.Conn, identity Identity, peer peerIdentity) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	reader := bufio.NewReader(conn)
	if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
		return
	}
	prefix, err := reader.Peek(1)
	if err == nil && prefix[0] == 'G' {
		prefix, err = reader.Peek(4)
	}
	if err != nil {
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	buffered := &bufferedClient{Conn: conn, reader: reader}
	if string(prefix) != "GET " {
		serveMessages(ctx, rpc.NewStream(buffered), identity, peer)
		return
	}
	serveUpgrade(ctx, buffered, identity, peer)
}

// A single authenticated connection is handed to net/http for bounded HTTP
// parsing and upgrade. Serve does not return until its handler has finished,
// including after hijacking, which http.Server.Shutdown does not wait for.
func serveUpgrade(ctx context.Context, conn net.Conn, identity Identity, peer peerIdentity) {
	l := &singleClient{conn: conn, done: make(chan struct{})}
	finished := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(finished); l.Close() }) }
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				finish()
			}
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Refused HTTP requests close after net/http flushes their response.
			// A successful upgrade replaces this with Connection: Upgrade.
			w.Header().Set("Connection", "close")
			if r.URL.Path != "/client" || r.URL.RawQuery != "" {
				http.NotFound(w, r)
				return
			}
			if serveWebSocket(ctx, w, r, identity, peer) {
				finish()
			}
		}),
	}
	// Closing the physical connection unblocks either header parsing or the
	// upgraded reader. Their termination, not cancellation alone, releases Serve.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = server.Serve(l)
	_ = server.Close()
	<-finished
}

type singleClient struct {
	conn     net.Conn
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *singleClient) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleClient) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *singleClient) Addr() net.Addr { return l.conn.LocalAddr() }

// The listener authenticates the physical peer before calling this function.
// WebSocket headers and JSON payloads never supply authority. The default
// upgrade policy refuses browser origins other than the request host.
func serveWebSocket(ctx context.Context, w http.ResponseWriter, r *http.Request, identity Identity, peer peerIdentity) bool {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return false
	}
	transport := rpc.NewWebSocket(conn)
	defer transport.Close()
	serveMessages(ctx, transport, identity, peer)
	return true
}
