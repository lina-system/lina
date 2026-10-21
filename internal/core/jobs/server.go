// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// Listen never reclaims an existing path. Core owns stale-socket recovery while
// holding its state-root lock. No worker credential is needed on this socket.
func Listen(path string) (*net.UnixListener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if err = ctlfiles.ValidatePrivateDirectory(info); err != nil {
		return nil, err
	}
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, jobapi.ErrUnavailable
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve owns its listener and connections. A disconnected worker does not
// abandon an admitted Aux call: Core persists its bounded result for replay.
// Cancellation by Core stops calls, closes connections and joins all handlers.
func (s *Service) Serve(ctx context.Context, l *net.UnixListener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var connections sync.WaitGroup
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096,
		BaseContext: func(net.Listener) context.Context { return ctx },
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				connections.Add(1)
			case http.StateClosed, http.StateHijacked:
				connections.Done()
			case http.StateActive, http.StateIdle: // The connection remains owned by this service.
			}
		},
	}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handle(ctx, w, r) })
	stopped := make(chan struct{})
	go func() { defer close(stopped); <-ctx.Done(); server.Close() }()
	err := server.Serve(l)
	cancel()
	<-stopped
	connections.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err

}
func (s *Service) handle(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var out any
	err := jobapi.ErrInvalid
	decode := func(v any) error {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		d.DisallowUnknownFields()
		if e := d.Decode(v); e != nil {
			return jobapi.ErrInvalid
		}
		if e := d.Decode(new(any)); e != io.EOF {
			return jobapi.ErrInvalid
		}
		return nil
	}
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/Aux":
			var v jobapi.AuxRequest
			if err = decode(&v); err == nil {
				out, err = s.Aux(ctx, v)
			}
		case "/Apply":
			var v jobapi.Proposal
			if err = decode(&v); err == nil {
				out, err = s.Apply(r.Context(), v)
			}
		case "/Report":
			var v jobapi.ReportRequest
			if err = decode(&v); err == nil {
				err = s.Report(r.Context(), v.JobID, v.State)
			}
		}
	}
	reply := jobapi.Reply{}
	if err != nil {
		reply.Error = jobapi.ErrUnavailable.Error()
		for _, known := range []error{jobapi.ErrInvalid, jobapi.ErrEpoch, jobapi.ErrConflict, jobapi.ErrDisabled, jobapi.ErrBudget, jobapi.ErrPending, jobapi.ErrNotDue} {
			if errors.Is(err, known) {
				reply.Error = known.Error()
				break
			}
		}
	} else {
		reply.Result, _ = json.Marshal(out)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}
