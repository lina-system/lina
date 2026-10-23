// SPDX-License-Identifier: Apache-2.0
package records

import "context"

// ViewRecord reads the canonical revision and checks ownership before returning
// presentation fields. Filesystem paths and front matter never reach a client.
func (s *Store) ViewRecord(ctx context.Context, person, id string) (Row, error) {
	r, revision, err := s.Read(ctx, id)
	if err != nil {
		return Row{}, err
	}
	d, err := parseDocument(r.Data)
	if err != nil {
		return Row{}, err
	}
	if person == "" || d.person != person {
		return Row{}, ErrNotFound
	}
	return Row{ID: id, Revision: revision, Kind: d.kind, State: d.state, Title: d.title, Note: d.note, Todo: d.todo}, nil
}
