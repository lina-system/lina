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
	"github.com/thisisjun786/lina/kit/loop"
)

// Tool references are read from the registered structured result shapes, never
// extracted from prose. A later forgotten source invalidates the whole turn,
// including the answer that depended on it, before any old result is exported.
func callReferences(ctx context.Context, tx *canon.Tx, scope Scope, call Call) error {
	if call.Result == nil || call.Result.Code != loop.OK {
		return nil
	}
	switch call.Call.Name {
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
	case "recall":
		var result struct {
			Results []struct {
				Kind, ID, Revision string
				Citations          []canon.Citation
			} `json:"results"`
		}
		if json.Unmarshal(call.Result.Output, &result) != nil {
			return Exclusion("missing_source")
		}
		for _, r := range result.Results {
			if err := recallReference(ctx, tx, scope, r.Kind, r.ID, r.Revision, r.Citations); err != nil {
				return err
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

// RecallVisible checks one recall result and its citations under the same
// rules as an exported episode. Records have no replay rule and are excluded.
func (s Source) RecallVisible(ctx context.Context, scope Scope, kind, id, revision string, citations []canon.Citation) error {
	return s.DB.Read(ctx, func(tx *canon.Tx) error {
		return recallReference(ctx, tx, scope, kind, id, revision, citations)
	})
}

func recallReference(ctx context.Context, tx *canon.Tx, scope Scope, kind, id, revision string, citations []canon.Citation) error {
	if err := sourceReference(ctx, tx, scope, kind, id, revision, map[string]bool{}); err != nil {
		return err
	}
	for _, c := range citations {
		if err := sourceReference(ctx, tx, scope, c.TargetKind, c.TargetID, c.TargetRevision, map[string]bool{}); err != nil {
			return err
		}
	}
	return nil
}

func sourceReference(ctx context.Context, tx *canon.Tx, scope Scope, kind, id, revision string, visiting map[string]bool) error {
	switch kind {
	case "event":
		return visible(ctx, tx, scope, id)
	case "memory", "summary":
		n, err := strconv.ParseInt(revision, 10, 64)
		if err != nil || n <= 0 {
			return Exclusion("missing_source")
		}
		if kind == "memory" {
			return memoryReference(ctx, tx, scope, id, n, visiting)
		}
		// A summary is readable only while every source it cites at that
		// revision is, down to the events it covers.
		key := "summary:" + id
		if visiting[key] {
			return Exclusion("missing_source")
		}
		visiting[key] = true
		defer delete(visiting, key)
		citations, err := canon.CitationsAt(ctx, tx, "summary", id, n)
		if err != nil {
			return err
		}
		if len(citations) == 0 {
			return Exclusion("missing_source")
		}
		for _, c := range citations {
			if err := sourceReference(ctx, tx, scope, c.TargetKind, c.TargetID, c.TargetRevision, visiting); err != nil {
				return err
			}
		}
		return nil
	}
	return Exclusion("missing_source")
}
func memoryReference(ctx context.Context, tx *canon.Tx, scope Scope, id string, revision int64, visiting map[string]bool) error {
	if revision <= 0 || visiting[id] {
		return Exclusion("missing_source")
	}
	visiting[id] = true
	defer delete(visiting, id)
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
