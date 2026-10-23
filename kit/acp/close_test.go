// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"context"
	"io"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/kit/acp"
)

type heldTeardown struct{ readStopped, release chan struct{} }

func (tr *heldTeardown) Read(context.Context) ([]byte, error) { <-tr.readStopped; return nil, io.EOF }
func (tr *heldTeardown) Write(context.Context, any) error     { return nil }
func (tr *heldTeardown) Close() error                         { close(tr.readStopped); <-tr.release; return nil }
func TestCloseWaitsForConcurrentTeardown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		tr := &heldTeardown{make(chan struct{}), make(chan struct{})}
		c := acp.New(ctx, tr)
		cancel()
		<-tr.readStopped
		for range c.Updates() {
		} // The reader has exited, but child teardown is held.
		closed := make(chan struct{})
		go func() { c.Close(); close(closed) }()
		synctest.Wait()
		early := false
		select {
		case <-closed:
			early = true
		default:
		}
		close(tr.release)
		<-closed
		if early {
			t.Fatal("Close returned before transport teardown completed")
		}
	})
}
func TestNullRequestID(t *testing.T) {
	answered := make(chan struct{})
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		a.send(permission(`null`, "s"))
		f := a.read()
		if string(f.ID) != "null" || f.Error != nil {
			t.Fatalf("null id response: %+v", f)
		}
		close(answered)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	select {
	case p, ok := <-c.Permissions():
		if !ok {
			t.Fatalf("null id rejected: %v", c.Err())
		}
		if e := p.Answer(ctx, "yes"); e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-answered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
