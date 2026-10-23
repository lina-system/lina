// SPDX-License-Identifier: Apache-2.0
package secrets

import "bytes"

// Contains checks untrusted service output/input against the credential issued
// to that service without releasing plaintext outside the credential boundary.
func (s *Store) Contains(key Key, body []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, ErrClosed
	}
	if !key.valid() {
		return false, ErrInvalid
	}
	value, err := s.backend.Read(key)
	defer clear(value)
	if err != nil {
		return false, storageError(err)
	}
	return len(value) > 0 && bytes.Contains(body, value), nil
}
