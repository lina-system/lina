// SPDX-License-Identifier: Apache-2.0
package recall

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// RestrictionCTE defines the shared restricted(kind,id) relation. Callers prefix
// it with WITH RECURSIVE and bind its arguments before their own. An empty CTE
// means this scope has no projection restrictions. Ownership and revision
// checks remain the caller's responsibility.
//
// First follow protected memory to its evidence, including original events and
// material bodies. Then exclude every dependent view, including replay copies
// and all view_of targets. Do not walk back down those dependent views: their
// other, independent sources remain readable. UNION over typed identities
// terminates cycles without conflating equal ids from different source kinds.
// Older memory producers without citation rows still have canonical evidence
// in their records; that evidence participates in the same relation.
func RestrictionCTE(scope Scope) (string, []any) {
	if !scope.Projection && !scope.ExternalModel {
		return "", nil
	}
	return `legacy_memory_edges(view_id,target_kind,target_id) AS (
 SELECT m.id,CASE json_extract(x.value,'$.kind') WHEN 'item' THEN 'memory' ELSE json_extract(x.value,'$.kind') END,
  json_extract(x.value,'$.id') FROM memory_item m,json_each(m.record,'$.evidence') x
 WHERE m.person=? AND NOT EXISTS(SELECT 1 FROM cite c WHERE c.view_kind='memory' AND c.view_id=m.id AND c.dropped_revision IS NULL)
), sensitive(kind,id) AS (
 SELECT 'memory',m.id FROM memory_item m WHERE m.person=?
  AND ((?=1 AND m.scope IN ('private','never-leaves')) OR (?=1 AND m.scope='never-leaves'))
 UNION
 SELECT c.target_kind,c.target_id FROM cite c JOIN sensitive s ON c.view_kind=s.kind AND c.view_id=s.id
  WHERE c.dropped_revision IS NULL
 UNION
 SELECT c.target_kind,c.target_id FROM legacy_memory_edges c JOIN sensitive s ON s.kind='memory' AND c.view_id=s.id
 UNION
 SELECT 'event',e.replay_of FROM conv_event e JOIN sensitive s ON s.kind='event' AND s.id=e.id
  WHERE e.replay_of IS NOT NULL
 UNION
 SELECT 'event',v.target_id FROM ev_rel v JOIN sensitive s ON s.kind='event' AND s.id=v.event_id
  WHERE v.kind='view_of'
 UNION
 SELECT 'material',e.material_id FROM conv_event e JOIN sensitive s ON s.kind='event' AND s.id=e.id
  WHERE e.material_id IS NOT NULL
), restricted(kind,id) AS (
 SELECT kind,id FROM sensitive
 UNION
 SELECT c.view_kind,c.view_id FROM cite c JOIN restricted r ON c.target_kind=r.kind AND c.target_id=r.id
  WHERE c.dropped_revision IS NULL
 UNION
 SELECT 'memory',c.view_id FROM legacy_memory_edges c JOIN restricted r ON c.target_kind=r.kind AND c.target_id=r.id
 UNION
 SELECT 'event',e.id FROM conv_event e JOIN restricted r ON r.kind='event' AND r.id=e.replay_of
 UNION
 SELECT 'event',v.event_id FROM ev_rel v JOIN restricted r ON r.kind='event' AND r.id=v.target_id
  WHERE v.kind='view_of'
 UNION
 SELECT 'event',e.id FROM conv_event e JOIN restricted r ON r.kind='material' AND r.id=e.material_id
)`, []any{scope.Person, scope.Person, boolInt(scope.Projection), boolInt(scope.ExternalModel)}
}

// SourceRestricted applies the same relation used before search ranking to an
// exact read or validation inside the caller's canonical snapshot.
func SourceRestricted(ctx context.Context, tx *canon.Tx, scope Scope, kind, id string) (bool, error) {
	cte, args := RestrictionCTE(scope)
	if cte == "" {
		return false, nil
	}
	var restricted bool
	err := tx.QueryRowContext(ctx, "WITH RECURSIVE "+cte+" SELECT EXISTS(SELECT 1 FROM restricted WHERE kind=? AND id=?)", append(args, kind, id)...).Scan(&restricted)
	return restricted, err
}
