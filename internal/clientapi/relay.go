// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

// SignInListener is a one-response loopback relay. It neither exchanges codes
// nor interprets OAuth state. Core validates the original URL, including dynamic
// registration parameters that the structured redirect cannot represent.
type SignInListener struct {
	listener  net.Listener
	server    *http.Server
	redirects chan protocol.SignInRedirect
	done      chan struct{}
	once      sync.Once
	stop      func() bool
}

func ListenSignIn(ctx context.Context) (*SignInListener, error) {
	return ListenSignInPort(ctx, 0)
}

// ListenSignInPort binds a registered callback port; zero requests an ephemeral port.
func ListenSignInPort(ctx context.Context, port int) (*SignInListener, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	r := &SignInListener{listener: listener, redirects: make(chan protocol.SignInRedirect, 1), done: make(chan struct{})}
	var consumed atomic.Bool
	r.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	r.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		if request.Method != http.MethodGet || request.URL.Path != "/auth/callback" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if len(request.RequestURI) > 16384 || request.URL.RawQuery == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !consumed.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		callback := "http://" + listener.Addr().String() + request.RequestURI
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		r.redirects <- protocol.SignInRedirect{URL: &callback}
	})
	r.stop = context.AfterFunc(ctx, func() { r.server.Close() })
	go func() { defer close(r.done); _ = r.server.Serve(listener) }()
	return r, nil
}
func (r *SignInListener) Port() int                                 { return r.listener.Addr().(*net.TCPAddr).Port }
func (r *SignInListener) Redirects() <-chan protocol.SignInRedirect { return r.redirects }
func (r *SignInListener) Close() {
	r.once.Do(func() { r.stop(); r.server.Close(); <-r.done })
}
