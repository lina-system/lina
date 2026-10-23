// SPDX-License-Identifier: Apache-2.0
package runs

import "context"

// Started reads the receipt for a trusted native call without admitting work.
// Recovery uses this when Core died between StartTool and saving its handle.
func (s *Service) Started(ctx context.Context, origin Origin, call string) (Record, bool, error) {
	rows, err := s.records(ctx)
	if err != nil {
		return Record{}, false, err
	}
	key := startIdentity(origin, call)
	for _, r := range rows {
		if r.StartKey == key {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}
