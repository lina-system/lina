// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"sync"
	"syscall"
	"time"
)

// A watcher records the whole command tree while it runs. The kernel reports
// every fork of an attached process through EVFILT_PROC, so a child is recorded
// even when its parent has already been reaped, and even when it changed its
// session or process group with POSIX_SPAWN_SETSID, which the Seatbelt policy
// cannot deny. Recorded identities are kept for the command's lifetime.
type watcher struct {
	kq       int
	leader   processIdentity
	mu       sync.Mutex
	tracked  map[uint64]struct{}
	attached map[int]struct{}
	scanErr  error
	done     chan struct{}
	stop     sync.Once
	wg       sync.WaitGroup
}

func newWatcher(leader processIdentity) (*watcher, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil, err
	}
	w := &watcher{kq: kq, leader: leader, tracked: make(map[uint64]struct{}), attached: make(map[int]struct{}), done: make(chan struct{})}
	if leader.uniqueID != 0 {
		w.tracked[leader.uniqueID] = struct{}{}
	}
	w.attach(leader.pid)
	w.wg.Add(1)
	go w.run()
	return w, nil
}

// run rescans on every notification. A fork or exit of an attached process is
// the only thing that can change the tree, so an idle command costs no scans.
func (w *watcher) run() {
	defer w.wg.Done()
	events := make([]syscall.Kevent_t, 64)
	for {
		select {
		case <-w.done:
			return
		default:
		}
		timeout := syscall.NsecToTimespec(int64(50 * time.Millisecond))
		n, err := syscall.Kevent(w.kq, nil, events, &timeout)
		if err != nil && err != syscall.EINTR && err != syscall.EBADF {
			time.Sleep(10 * time.Millisecond)
		}
		for _, event := range events[:n] {
			if event.Flags&syscall.EV_EOF != 0 {
				w.detach(int(event.Ident))
			}
		}
		if n > 0 || err != nil {
			w.scan()
		}
	}
}

// attach subscribes to one process. A process that refuses the subscription,
// because it left the same user, is still covered by the token sweep.
func (w *watcher) attach(pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.attachLocked(pid)
}

func (w *watcher) attachLocked(pid int) {
	if pid <= 1 {
		return
	}
	if _, ok := w.attached[pid]; ok {
		return
	}
	change := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ENABLE | syscall.EV_CLEAR,
		Fflags: syscall.NOTE_FORK | syscall.NOTE_EXIT,
	}
	if _, err := syscall.Kevent(w.kq, []syscall.Kevent_t{change}, nil, nil); err != nil {
		return
	}
	w.attached[pid] = struct{}{}
}

func (w *watcher) detach(pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.attached, pid)
}

// scan records every process reachable from the tracked identities, including
// one whose parent was recorded earlier and has since been reaped.
func (w *watcher) scan() {
	states, err := processStates()
	if err != nil {
		w.mu.Lock()
		w.scanErr = err
		w.mu.Unlock()
		return
	}
	identities := make([]processIdentity, len(states))
	for i, state := range states {
		identities[i] = state.processIdentity
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	trackDescendants(w.tracked, identities)
	for _, state := range states {
		if _, ok := w.tracked[state.uniqueID]; ok {
			w.attachLocked(state.pid)
		}
	}
}

func (w *watcher) close() {
	w.stop.Do(func() {
		close(w.done)
		_ = syscall.Close(w.kq)
		w.wg.Wait()
	})
}
