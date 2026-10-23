// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/rpc"
)

// Start starts an owned stdio child. The caller supplies an absolute executable,
// working directory, environment and lease; no login, settings or environment
// policy is changed here. Stdin/Stdout are replaced with protocol pipes. Call
// Initialize to confirm startup and negotiate version 1. Close reaps the child
// and its descendants. The executable must register proc.SuperviseMain.
func Start(ctx context.Context, l lease.Lease, command proc.Command) (*Client, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	owner := proc.NewOwner()
	conn := &stdioConn{reader: outR, writer: inW, cancel: cancel, owner: owner, exited: make(chan struct{})}
	command.Stdin = inR
	command.Stdout = outW
	if command.Stderr == nil {
		command.Stderr = io.Discard
	}
	go func() {
		defer close(conn.exited)
		defer inR.Close()
		defer outW.Close()
		_ = owner.Run(life, l, command)
	}()
	return New(ctx, rpc.NewStream(conn)), nil
}

type stdioConn struct {
	reader, writer *os.File
	cancel         context.CancelFunc
	owner          *proc.Owner
	exited         chan struct{}
	once           sync.Once
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *stdioConn) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.reader.Close()
		c.writer.Close()
		<-c.exited
		c.owner.Close(context.Background())
	})
	return nil
}
func (c *stdioConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
func (c *stdioConn) SetReadDeadline(t time.Time) error  { return c.reader.SetReadDeadline(t) }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return c.writer.SetWriteDeadline(t) }
func (c *stdioConn) LocalAddr() net.Addr                { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr               { return stdioAddr{} }

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }
