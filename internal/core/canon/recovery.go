// SPDX-License-Identifier: Apache-2.0
package canon

// Reconcile excludes readers while a durable external removal journal and canon
// are brought into agreement. A failure keeps reads closed until a successful
// reconciliation. The callback may use Write, but must not call Read or nest
// Reconcile. Startup must reconcile before publishing this Store to consumers.
// Existing reads finish first; no reader sees a partially replayed operation.
func (s *Store) Reconcile(fn func() error) error {
	s.readGate.Lock()
	defer s.readGate.Unlock()
	if fn == nil {
		return ErrInvalid
	}
	s.readFailure = fn()
	return s.readFailure
}
