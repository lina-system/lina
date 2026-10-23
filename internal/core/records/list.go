// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"sort"
)

// List returns canonical files, after the same recovery/outside-change checks as Read.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	_, files, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	seen := map[string]bool{}
	for name, hash := range files {
		data, err := s.readObject(hash, "blob")
		if err != nil {
			return nil, err
		}
		id, err := recordID(data)
		if err != nil || seen[id] || !validPath(name) {
			return nil, ErrRecordsChanged
		}
		seen[id] = true
		out = append(out, Record{ID: id, Path: name, Data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
