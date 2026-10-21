// SPDX-License-Identifier: Apache-2.0
package canon

import "context"

// RecoveryWrite is valid only within its Reconcile callback. It runs through
// the same fenced writer as ordinary writes while excluding other operations.
type RecoveryWrite func(context.Context, func(*Tx) error) error

// Reconcile drains existing operations and excludes ordinary readers/writers
// while the removal journal and canon are brought into agreement. A failure
// keeps access closed until replay succeeds. The callback uses only the given
// writer, never Read, Write or a nested Reconcile, and must not retain it.
// Startup reconciles before publishing this Store to consumers.
func (s *Store) Reconcile(fn func(RecoveryWrite) error) error {
	s.readGate.Lock()
	defer s.readGate.Unlock()
	if fn == nil {
		return ErrInvalid
	}
	s.readFailure = ErrRecoveryRequired // Also fail closed if the callback panics.
	s.readFailure = fn(func(ctx context.Context, write func(*Tx) error) error {
		if write == nil {
			return ErrInvalid
		}
		return s.submit(ctx, write, nil)
	})
	return s.readFailure
}
