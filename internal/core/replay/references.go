// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/kit/loop"
)

func sourceRestriction(ctx context.Context, tx *canon.Tx, scope Scope, kind, id string) error {
	restricted, err := recall.SourceRestricted(ctx, tx, recall.Scope{Person: scope.Person, Projection: true}, kind, id)
	if err != nil {
		return err
	}
	if restricted {
		return Exclusion("restricted_content")
	}
	return nil
}

// Tool references are read from the registered structured result shapes, never
// extracted from prose. A later forgotten source invalidates the whole turn,
// including the answer that depended on it, before any old result is exported.
func callReferences(ctx context.Context, tx *canon.Tx, scope Scope, call Call) error {
	if call.Result == nil || call.Result.Code != loop.OK {
		return nil
	}
	switch call.Call.Name {
	case "recall":
		var result struct {
			Results []struct {
				Kind, ID, Revision string
				Citations          []canon.Citation
			} `json:"results"`
		}
		if json.Unmarshal(call.Result.Output, &result) != nil || result.Results == nil {
			return Exclusion("missing_source")
		}
		for _, hit := range result.Results {
			if err := recallReference(ctx, tx, scope, hit.Kind, hit.ID, hit.Revision, map[string]bool{}); err != nil {
				return err
			}
			// Keep checking the recorded citations even if today's source has
			// dropped one: its old excerpt is still part of this episode.
			for _, ref := range hit.Citations {
				if err := recallReference(ctx, tx, scope, ref.TargetKind, ref.TargetID, ref.TargetRevision, map[string]bool{}); err != nil {
					return err
				}
			}
		}
	case "recall_person":
		var answer struct {
			Sources []struct {
				ID       string
				Revision int64
				Evidence []memory.Evidence
			}
			Answer []struct {
				Citations []struct {
					ID       string
					Revision int64
				}
			}
		}
		if json.Unmarshal(call.Result.Output, &answer) != nil {
			return Exclusion("missing_source")
		}
		check := func(id string, revision int64) error {
			if event, ok := strings.CutPrefix(id, "event:"); ok {
				return visible(ctx, tx, scope, event)
			}
			if item, ok := strings.CutPrefix(id, "memory:"); ok {
				return memoryReference(ctx, tx, scope, item, revision, map[string]bool{})
			}
			return Exclusion("missing_source")
		}
		for _, source := range answer.Sources {
			if err := check(source.ID, source.Revision); err != nil {
				return err
			}
			for _, ref := range source.Evidence {
				if err := evidenceReference(ctx, tx, scope, ref, map[string]bool{}); err != nil {
					return err
				}
			}
		}
		for _, sentence := range answer.Answer {
			for _, citation := range sentence.Citations {
				if err := check(citation.ID, citation.Revision); err != nil {
					return err
				}
			}
		}
	case "expand":
		var result struct {
			Events []Event `json:"events"`
		}
		if json.Unmarshal(call.Result.Output, &result) != nil {
			return Exclusion("missing_source")
		}
		for _, event := range result.Events {
			if err := visible(ctx, tx, scope, event.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecallVisible checks a live recall result and its citations with the same
// source and dependency validation used for recorded results during export.
func (s Source) RecallVisible(ctx context.Context, scope Scope, kind, id, revision string, citations []canon.Citation) error {
	return s.DB.Read(ctx, func(tx *canon.Tx) error {
		if err := recallReference(ctx, tx, scope, kind, id, revision, map[string]bool{}); err != nil {
			return err
		}
		for _, ref := range citations {
			if err := recallReference(ctx, tx, scope, ref.TargetKind, ref.TargetID, ref.TargetRevision, map[string]bool{}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Validate current identity/revision and the canonical citation graph. Neither
// an index row nor an old tool excerpt authorizes exporting a removed source.
func recallReference(ctx context.Context, tx *canon.Tx, scope Scope, kind, id, revision string, visiting map[string]bool) error {
	key := kind + ":" + id
	if id == "" || revision == "" || visiting[key] || len(visiting) >= 128 {
		return Exclusion("missing_source")
	}
	visiting[key] = true
	defer delete(visiting, key)
	if kind == "material" {
		version, err := strconv.ParseInt(revision, 10, 64)
		if err != nil || version <= 0 {
			return Exclusion("missing_source")
		}
		return evidenceReference(ctx, tx, scope, memory.Evidence{Kind: memory.Material, ID: id, Revision: version}, map[string]bool{})
	}
	switch kind {
	case "event", "memory", "summary", "record":
	default:
		return Exclusion("missing_source")
	}
	var present int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recall_source WHERE kind=? AND source_id=? AND revision=? AND person=? AND (holder=? OR holder='all')`, kind, id, revision, scope.Person, scope.Agent).Scan(&present)
	if err != nil {
		return err
	}
	if present != 1 {
		return Exclusion("restricted_content")
	}
	switch kind {
	case "event":
		return visible(ctx, tx, scope, id)
	case "memory":
		version, err := strconv.ParseInt(revision, 10, 64)
		if err != nil {
			return Exclusion("missing_source")
		}
		return memoryReference(ctx, tx, scope, id, version, map[string]bool{})
	}
	if err := sourceRestriction(ctx, tx, scope, kind, id); err != nil {
		return err
	}
	var refs []canon.Citation
	if kind == "summary" {
		version, parseErr := strconv.ParseInt(revision, 10, 64)
		if parseErr != nil {
			return Exclusion("missing_source")
		}
		refs, err = canon.CitationsAt(ctx, tx, kind, id, version)
	} else {
		rows, queryErr := tx.QueryContext(ctx, `SELECT target_kind,target_id,target_revision FROM cite WHERE view_kind='record' AND view_id=? AND dropped_revision IS NULL`, id)
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			var ref canon.Citation
			if err = rows.Scan(&ref.TargetKind, &ref.TargetID, &ref.TargetRevision); err != nil {
				break
			}
			refs = append(refs, ref)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := recallReference(ctx, tx, scope, ref.TargetKind, ref.TargetID, ref.TargetRevision, visiting); err != nil {
			return err
		}
	}
	return nil
}
func memoryReference(ctx context.Context, tx *canon.Tx, scope Scope, id string, revision int64, visiting map[string]bool) error {
	if revision <= 0 || visiting[id] {
		return Exclusion("missing_source")
	}
	visiting[id] = true
	defer delete(visiting, id)
	if err := sourceRestriction(ctx, tx, scope, "memory", id); err != nil {
		return err
	}
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT r.record FROM retract_visible_memory m JOIN memory_revision r ON r.item_id=m.id
 WHERE m.id=? AND r.revision=? AND m.person=? AND (m.holder='all' OR m.holder=?) AND m.state='active' AND m.scope='local'`, id, revision, scope.Person, scope.Agent).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Exclusion("restricted_content")
	}
	if err != nil {
		return err
	}
	var item memory.Item
	if json.Unmarshal(data, &item) != nil || item.Scope != memory.Local || item.State != memory.Active {
		return Exclusion("restricted_content")
	}
	for _, ref := range item.Evidence {
		evidenceScope := scope
		if item.Kind == memory.Name && item.Holder == memory.All && ref.Kind == memory.Event {
			// A readable common name authorizes checking its provenance, not
			// reading another agent's turn. Bind the source agent from canon;
			// visible still enforces person ownership, privacy and removal.
			err := tx.QueryRowContext(ctx, `SELECT agent_id FROM conv_event WHERE id=? AND person_id=? AND speaker_kind='person'`, ref.ID, scope.Person).Scan(&evidenceScope.Agent)
			if errors.Is(err, sql.ErrNoRows) {
				return Exclusion("restricted_content")
			}
			if err != nil {
				return err
			}
		}
		if err := evidenceReference(ctx, tx, evidenceScope, ref, visiting); err != nil {
			return err
		}
	}
	return nil
}
func evidenceReference(ctx context.Context, tx *canon.Tx, scope Scope, ref memory.Evidence, visiting map[string]bool) error {
	switch ref.Kind {
	case memory.Event:
		return visible(ctx, tx, scope, ref.ID)
	case memory.MemoryItem:
		return memoryReference(ctx, tx, scope, ref.ID, ref.Revision, visiting)
	case memory.Material:
		if err := sourceRestriction(ctx, tx, scope, "material", ref.ID); err != nil {
			return err
		}
		var n int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM assets a JOIN asset_revisions r ON r.asset_id=a.id WHERE a.id=? AND r.revision=? AND a.person_id=? AND a.agent_id=? AND a.trashed_at=0`, ref.ID, ref.Revision, scope.Person, scope.Agent).Scan(&n)
		if err != nil {
			return err
		}
		if n != 1 {
			return Exclusion("restricted_content")
		}
		return nil
	case memory.WorkResult, memory.External:
		return Exclusion("missing_source")
	}
	return Exclusion("missing_source")
}
