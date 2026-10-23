// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"context"
	"io"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/kit/acp"
)

// Model stdio teardown: closing the pipes unblocks Read before the owned
// process tree has been reaped and its lease released.
type delayedCloseTransport struct {
	closing, release chan struct{}
}

func (tr *delayedCloseTransport) Read(context.Context) ([]byte, error) {
	<-tr.closing
	return nil, io.EOF
}
func (tr *delayedCloseTransport) Write(context.Context, any) error { return io.ErrClosedPipe }
func (tr *delayedCloseTransport) Close() error {
	close(tr.closing)
	<-tr.release
	return nil
}

func TestCloseWaitsForTransportTeardown(t *testing.T) {
	for _, cause := range []string{"connection-cancel", "write-failure"} {
		t.Run(cause, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				tr := &delayedCloseTransport{make(chan struct{}), make(chan struct{})}
				c := acp.New(ctx, tr)
				if cause == "connection-cancel" {
					cancel()
				} else {
					go func() {
						if _, err := c.Initialize(ctx, acp.Implementation{Name: "test"}); err == nil {
							t.Error("write failure succeeded")
						}
					}()
				}
				<-tr.closing
				closed := make(chan error, 2)
				for range 2 {
					go func() { closed <- c.Close() }()
				}
				synctest.Wait()
				// Every goroutine is now blocked or finished. No real-time wait
				// or scheduling assumption can hide a premature Close return.
				if len(closed) != 0 {
					t.Error("Close returned before transport teardown finished")
				}
				close(tr.release)
				for range 2 {
					if err := <-closed; err != nil {
						t.Fatal(err)
					}
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
