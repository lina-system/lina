// SPDX-License-Identifier: Apache-2.0
package memory

import "context"

// CurrentRelationship is an observer's current view of one person. Missing
// views stay absent. Apply the same removal, validity and endpoint-scope filters
// as recall, selecting relationship kind and the exact holder before the limit.
func (s *Store) CurrentRelationship(ctx context.Context, scope RecallScope, at int64) ([]Hit, error) {
	if scope.Projection {
		return nil, nil
	}
	return s.recall(ctx, scope, Query{At: at, Limit: 1, PersonOnly: true}, false, true)
}
