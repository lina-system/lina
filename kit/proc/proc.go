// SPDX-License-Identifier: Apache-2.0

// Package proc owns child processes and their complete execution lifetime.
package proc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
)

var ErrClosed = errors.New("process owner is closed")

// Command has no implicit environment or descriptors. Path must be absolute.
// ExtraFiles are reserved for trusted launchers, such as bubblewrap's BPF FD.
type Command struct {
	Path           string
	Args           []string
	Dir            string
	Env            []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	ExtraFiles     []*os.File
}
type child struct {
	process process
	done    chan struct{}
}

type process interface {
	start() error
	kill() error
	wait() error
	release()
}

func command(c Command) *exec.Cmd {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append([]string{}, c.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	cmd.ExtraFiles = c.ExtraFiles
	cmd.WaitDelay = time.Second
	return cmd
}

type Owner struct {
	mu       sync.Mutex
	closed   bool
	children map[*child]struct{}
}

// NewOwner owns commands independently of their process groups. Linux hosts
// register SuperviseMain as the explicit proc-supervise subcommand and macOS
// hosts register JobMain as proc-job (see runtime.md).
func NewOwner() *Owner { return &Owner{children: make(map[*child]struct{})} }

// Run retains the supplied lease until the command has been reaped. Cancellation
// and Close terminate its descendants, including children holding stdio open.
func (o *Owner) Run(ctx context.Context, l lease.Lease, c Command) error {
	return lease.Do(ctx, l, func(ctx context.Context) error {
		if !filepath.IsAbs(c.Path) {
			return errors.New("process executable must be absolute")
		}
		p, err := newProcess(c)
		if err != nil {
			return err
		}
		defer p.release()
		ch := &child{process: p, done: make(chan struct{})}
		o.mu.Lock()
		if o.closed {
			o.mu.Unlock()
			return ErrClosed
		}
		if err := ctx.Err(); err != nil {
			o.mu.Unlock()
			return err
		}
		if err := p.start(); err != nil {
			o.mu.Unlock()
			return err
		}
		o.children[ch] = struct{}{}
		o.mu.Unlock()
		finished, canceled := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(canceled)
			select {
			case <-ctx.Done():
				_ = p.kill()
			case <-finished:
			}
		}()
		err = p.wait()
		close(finished)
		<-canceled
		o.mu.Lock()
		delete(o.children, ch)
		close(ch.done)
		o.mu.Unlock()
		if err == nil {
			return ctx.Err()
		}
		return err
	})
}

// Close is idempotent, rejects new work, and joins every registered command.
// A deadline bounds the wait but does not release live commands' leases.
func (o *Owner) Close(ctx context.Context) error {
	o.mu.Lock()
	o.closed = true
	children := make([]*child, 0, len(o.children))
	for ch := range o.children {
		_ = ch.process.kill()
		children = append(children, ch)
	}
	o.mu.Unlock()
	for _, ch := range children {
		select {
		case <-ch.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
