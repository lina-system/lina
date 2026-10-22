// SPDX-License-Identifier: Apache-2.0
package canon

// Changed closes after a committed source mutation. Subscribe before reading
// projections, then subscribe again after waking. Reads and rollbacks are quiet.
func (s *Store) Changed() <-chan struct{} {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *Store) notifyChanged() {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if s.changed != nil {
		close(s.changed)
	}
	s.changed = make(chan struct{})
}
