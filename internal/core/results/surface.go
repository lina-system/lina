// SPDX-License-Identifier: Apache-2.0

package results

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/kit/result"
)

// PresentSurface folds privately, then uses Present's transactional revision
// comparison. A concurrent update can only make the whole batch stale.
func (s *Store) PresentSurface(ctx context.Context, scope result.Scope, p result.PresentRequest) (result.Card, error) {
	if p.Type != result.SurfaceType {
		return result.Card{}, result.ErrSurfaceValidation
	}
	var batch result.SurfaceBatch
	if result.Decode(p.Body, &batch) != nil {
		return result.Card{}, result.ErrSurfaceValidation
	}
	var previous *result.Surface
	if p.ID != "" {
		old, err := s.Read(ctx, scope, result.ReadRequest{ID: p.ID})
		if err != nil {
			return result.Card{}, err
		}
		if old.Revision != p.BaseRevision {
			return result.Card{}, ErrStaleRevision
		}
		if old.Type != result.SurfaceType {
			return result.Card{}, ErrOutOfScope
		}
		body, err := result.Validate(old.Type, old.Body, result.Limits{})
		if err != nil {
			return result.Card{}, err
		}
		previous = body.(*result.Surface)
	}
	folded, err := result.FoldSurface(previous, batch)
	if err != nil {
		return result.Card{}, err
	}
	p.Body, err = json.Marshal(folded)
	if err != nil {
		return result.Card{}, err
	}
	return s.Present(ctx, scope, p)
}
