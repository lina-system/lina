// SPDX-License-Identifier: Apache-2.0

// Package proc owns child process groups and their complete execution lifetime.
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
	command *exec.Cmd
	done    chan struct{}
}
type Owner struct {
	mu       sync.Mutex
	closed   bool
	children map[*child]struct{}
}

func NewOwner() *Owner { return &Owner{children: make(map[*child]struct{})} }

// Run retains the supplied lease until the command has been reaped. Cancellation
// and Close kill its entire process group, including children holding stdio open.
func (o *Owner) Run(ctx context.Context, l lease.Lease, c Command) error {
	return lease.Do(ctx, l, func(ctx context.Context) error {
		if !filepath.IsAbs(c.Path) {
			return errors.New("process executable must be absolute")
		}
		cmd := exec.CommandContext(ctx, c.Path, c.Args...)
		cmd.Dir = c.Dir
		cmd.Env = append([]string{}, c.Env...)
		cmd.Stdin = c.Stdin
		cmd.Stdout = c.Stdout
		cmd.Stderr = c.Stderr
		cmd.ExtraFiles = c.ExtraFiles
		if err := configure(cmd); err != nil {
			return err
		}
		cmd.Cancel = func() error { return kill(cmd) }
		cmd.WaitDelay = time.Second
		ch := &child{command: cmd, done: make(chan struct{})}
		o.mu.Lock()
		if o.closed {
			o.mu.Unlock()
			return ErrClosed
		}
		if err := cmd.Start(); err != nil {
			o.mu.Unlock()
			return err
		}
		o.children[ch] = struct{}{}
		o.mu.Unlock()
		err := cmd.Wait()
		o.mu.Lock()
		// Also remove descendants left by a command that exited successfully.
		_ = kill(cmd)
		delete(o.children, ch)
		close(ch.done)
		o.mu.Unlock()
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
		_ = kill(ch.command)
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
