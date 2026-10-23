// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
)

var errCandidatePending = errors.New("candidate_pending")

type candidateMaterial struct {
	ID       string
	Revision int64
}
type candidateBodies map[candidateMaterial]string

// Resolve only immutable metadata while reading; file-backed bodies are loaded
// and verified by Core's material adapter outside the canon transaction.
func (s *Store) candidateBodies(ctx context.Context, a Actor, id string) candidateBodies {
	bodies := candidateBodies{}
	var refs []candidateMaterial
	_ = s.db.Read(ctx, func(tx *canon.Tx) error {
		_, _, err := retainedCandidateSource(ctx, tx, a, id, func(ref candidateMaterial) (string, error) { refs = append(refs, ref); return "", nil })
		return err
	})
	if s.ReadCandidateMaterial != nil {
		for _, ref := range refs {
			if body, err := s.ReadCandidateMaterial(ctx, ref.ID, ref.Revision); err == nil {
				bodies[ref] = body
			}
		}
	}
	return bodies
}

type candidateSource struct {
	ID, Person, Agent, Speaker, Body, Origin, Scaffold string
	Entry                                              int
	Visible, Secret                                    bool
}

func retainedCandidateSource(ctx context.Context, tx *canon.Tx, a Actor, id string, material func(candidateMaterial) (string, error)) (candidateSource, string, error) {
	seen := map[string]bool{}
	var first *candidateSource
	for len(seen) < 64 {
		if seen[id] {
			return candidateSource{}, "", errCandidatePending
		}
		seen[id] = true
		var e candidateSource
		var ref candidateMaterial
		var replay string
		err := tx.QueryRowContext(ctx, `SELECT e.id,coalesce(e.person_id,''),e.agent_id,e.speaker_kind,coalesce(e.body,''),
   coalesce(e.material_id,''),coalesce(e.material_revision,0),coalesce(e.replay_of,''),coalesce(e.scaffold_kind,''),coalesce(json_extract(e.origin,'$.kind'),''),CAST(coalesce(json_extract(e.origin,'$.entry'),0) AS INTEGER),
   EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=e.id),EXISTS(SELECT 1 FROM ev_span s WHERE s.event_id=e.id AND s.sensitivity='secret')
   FROM conv_event e WHERE e.id=?`, id).Scan(&e.ID, &e.Person, &e.Agent, &e.Speaker, &e.Body, &ref.ID, &ref.Revision, &replay, &e.Scaffold, &e.Origin, &e.Entry, &e.Visible, &e.Secret)
		if err != nil {
			return e, "", err
		}
		if !e.Visible || e.Secret || e.Person != a.Person || e.Agent != a.Agent || e.Speaker != "person" || e.Origin != "native" || e.Entry != int(guard.EntryOwner) || e.Scaffold != "" {
			return e, "", errCandidatePending
		}
		var blocked int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_block WHERE person=? AND (event_id=? OR conversation_id=(SELECT conversation_id FROM conv_event WHERE id=?))", e.Person, id, id).Scan(&blocked); err != nil {
			return e, "", err
		}
		if blocked != 0 {
			return e, "", errCandidatePending
		}
		if first == nil {
			copy := e
			first = &copy
		} else if first.Person != e.Person || first.Agent != e.Agent || first.Speaker != e.Speaker {
			return e, "", errCandidatePending
		}
		rows, err := tx.QueryContext(ctx, "SELECT target_id FROM ev_rel WHERE event_id=? AND kind='view_of'", id)
		if err != nil {
			return e, "", err
		}
		for rows.Next() {
			var target string
			if err = rows.Scan(&target); err != nil {
				rows.Close()
				return e, "", err
			}
			if replay != "" && replay != target {
				rows.Close()
				return e, "", errCandidatePending
			}
			replay = target
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return e, "", err
		}
		if replay != "" {
			id = replay
			continue
		}
		if ref.ID != "" {
			e.Body, err = material(ref)
			if err != nil {
				return e, "", err
			}
		}
		return e, e.Body, nil
	}
	return candidateSource{}, "", errCandidatePending
}

// A deliberately literal recognizer proves owner-directed commitments and
// requests. Other wording is still saved as a candidate; model confidence and
// model-supplied labels cannot turn uncertain language into an automatic task.
func candidateAction(quote string) (class, action string) {
	q := strings.TrimSpace(quote)
	lower := strings.ToLower(q)
	for _, prefix := range []string{"i will ", "i'll ", "i promise to "} {
		if strings.HasPrefix(lower, prefix) {
			class, action = "promise", q[len(prefix):]
			break
		}
	}
	if class == "" {
		for _, prefix := range []string{"remind me to ", "i need to "} {
			if strings.HasPrefix(lower, prefix) {
				class, action = "request", q[len(prefix):]
				break
			}
		}
	}
	if class == "" {
		for _, prefix := range []string{"내가 ", "나는 "} {
			if rest, ok := strings.CutPrefix(q, prefix); ok {
				rest = strings.TrimSuffix(rest, ".")
				for _, suffix := range []string{"할게", "하겠다", "하겠습니다"} {
					if action, ok = strings.CutSuffix(rest, suffix); ok {
						return "promise", strings.TrimSpace(action) + "하기"
					}
				}
			}
		}
		return "", ""
	}
	action = strings.TrimSpace(strings.TrimRight(action, ".!"))
	a := strings.ToLower(action)
	for _, prefix := range []string{"not ", "never ", "maybe ", "possibly ", "probably ", "try to ", "consider "} {
		if strings.HasPrefix(a, prefix) {
			return "", ""
		}
	}
	if action == "" || strings.ContainsAny(action, "\n\r?\"`") || strings.Contains(a, " if ") {
		return "", ""
	}
	return class, action
}

func verifyCandidateSource(ctx context.Context, tx *canon.Tx, a Actor, in CandidateInput, bodies candidateBodies) (string, string, error) {
	e, body, err := retainedCandidateSource(ctx, tx, a, in.Source, func(ref candidateMaterial) (string, error) {
		body, ok := bodies[ref]
		if !ok {
			return "", errCandidatePending
		}
		return body, nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errCandidatePending) {
			return "", "", errCandidatePending
		}
		return "", "", err
	}
	if e.Speaker != "person" || e.Person != a.Person || e.Agent != a.Agent || e.Origin != "native" || e.Entry != int(guard.EntryOwner) || e.Scaffold != "" {
		return "", "", errCandidatePending
	}
	quote := strings.TrimSpace(body)
	if quote == "" || quote != strings.TrimSpace(in.Quotation) {
		return "", "", errCandidatePending
	}
	class, action := candidateAction(quote)
	if class == "" || equivalentTitle(action) != equivalentTitle(in.Title) {
		return "", "", errCandidatePending
	}
	hash := sha256.Sum256([]byte(e.ID + "\x00" + e.Person + "\x00" + e.Agent + "\x00" + quote))
	return class, hex.EncodeToString(hash[:]), nil
}
