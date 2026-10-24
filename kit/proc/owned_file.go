// SPDX-License-Identifier: Apache-2.0

//go:build unix

package proc

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// copyOwnedFile copies src to dst until src ends, the write fails, or stop is
// closed. It does not close src. A pollable file, such as a pipe, is released
// by a read deadline, and that deadline is cleared before return so the caller
// keeps the descriptor and any bytes still waiting in it. A regular file has
// no deadline; it is copied through to EOF instead of being closed.
func copyOwnedFile(dst io.Writer, src *os.File, stop <-chan struct{}) error {
	if err := src.SetReadDeadline(time.Time{}); err != nil {
		_, err := io.Copy(dst, src)
		return err
	}
	if stop == nil {
		defer func() { _ = src.SetReadDeadline(time.Time{}) }()
		_, err := io.Copy(dst, src)
		return err
	}
	copyDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-stop:
			// A deadline already in the past unblocks a read that is waiting
			// for a writer which is still open. time.Now can lose that race.
			_ = src.SetReadDeadline(time.Unix(1, 0))
		case <-copyDone:
		}
	}()
	_, err := io.Copy(dst, src)
	close(copyDone)
	<-watcherDone
	clearErr := src.SetReadDeadline(time.Time{})
	select {
	case <-stop:
		if errors.Is(err, os.ErrDeadlineExceeded) {
			err = nil
		}
	default:
	}
	if clearErr != nil {
		err = errors.Join(err, clearErr)
	}
	return err
}

// ownedFileRelay is the cancellable copy of one caller-owned file.
type ownedFileRelay struct {
	stop chan struct{}
	once sync.Once
	done chan struct{}
}

func newOwnedFileRelay() *ownedFileRelay {
	return &ownedFileRelay{stop: make(chan struct{}), done: make(chan struct{})}
}

// stopAndWait asks the copy to return and waits for it. A nil relay is a
// reader that is not a file; that copy is not joined here.
func (r *ownedFileRelay) stopAndWait(limit time.Duration) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() { close(r.stop) })
	if limit < 0 {
		limit = 0
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-r.done:
		return nil
	case <-timer.C:
		select {
		case <-r.done:
			return nil
		default:
			return errors.New("process input did not finish")
		}
	}
}
