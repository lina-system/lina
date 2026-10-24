// SPDX-License-Identifier: Apache-2.0
package recall

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

const VectorCandidates = 100

type Embedder interface {
	Config() embedding.Config
	Embed(context.Context, []string) ([][]float32, error)
}

// Vectors is Core-owned. Its queue is durable, while the worker receives only
// a processed count. A request cannot supply document text or a destination.
type Vectors struct {
	db        Canon
	readEvent ReadEvent
	adapter   Embedder
	mu        sync.Mutex
	after     int64
}

func NewVectors(ctx context.Context, db Canon, readEvent ReadEvent, adapter Embedder) (*Vectors, error) {
	v := &Vectors{db: db, readEvent: readEvent, adapter: adapter}
	if adapter == nil {
		return v, nil
	}
	cfg := adapter.Config().Defaults()
	if cfg.Validate() != nil {
		return nil, embedding.ErrInvalid
	}
	err := db.Write(ctx, func(tx *canon.Tx) error {
		var model, endpoint string
		var dimension int
		if err := tx.QueryRowContext(ctx, "SELECT model,dimension,endpoint FROM search_index_config WHERE name='recall'").Scan(&model, &dimension, &endpoint); err != nil {
			return err
		}
		if model == cfg.Model && dimension == cfg.Dimension && endpoint == cfg.Endpoint {
			return nil
		}
		// Only derived vectors are replaced; the DDL and generation switch are atomic.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DROP TABLE recall_vec; CREATE VIRTUAL TABLE recall_vec USING vec0(embedding float[%d] distance_metric=cosine)", cfg.Dimension)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM recall_embed_pending; INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE search_index_config SET model=?,dimension=?,endpoint=?,generation=generation+1,ready=0 WHERE name='recall'", cfg.Model, cfg.Dimension, cfg.Endpoint)
		return err
	})
	return v, err
}

type embeddingDocument struct {
	sequence, id             int64
	kind, source, hash, text string
	external                 bool
	window                   bool
	spans                    []secrets.Span
}

func (v *Vectors) generation(ctx context.Context, tx *canon.Tx) (int64, bool, error) {
	var generation int64
	var ready, dimension int
	var model, endpoint string
	err := tx.QueryRowContext(ctx, "SELECT generation,ready,model,dimension,endpoint FROM search_index_config WHERE name='recall'").Scan(&generation, &ready, &model, &dimension, &endpoint)
	cfg := v.adapter.Config().Defaults()
	if err == nil && (model != cfg.Model || dimension != cfg.Dimension || endpoint != cfg.Endpoint) {
		err = embedding.ErrUnavailable
	}
	return generation, ready == 1, err
}

// DrainBatch makes at most one bounded outbound call, outside the writer. Failed
// rows remain queued; a cursor allows later rows to advance. Source replacement,
// consent changes and model switches invalidate every corresponding late result.
func (v *Vectors) DrainBatch(ctx context.Context) (int, error) {
	if v == nil || v.adapter == nil {
		return 0, nil
	}
	if !v.mu.TryLock() {
		return 0, nil
	}
	defer v.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	var batch []embeddingDocument
	var generation int64
	err := v.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		generation, _, err = v.generation(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT q.sequence,d.id,d.kind,d.source_id,d.text_hash FROM recall_embed_pending q JOIN recall_doc d ON d.id=q.doc WHERE q.sequence>? ORDER BY q.sequence LIMIT ?`, v.after, embedding.BatchSize)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d embeddingDocument
			if err = rows.Scan(&d.sequence, &d.id, &d.kind, &d.source, &d.hash); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, d)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for i := range batch {
			d := &batch[i]
			var eligible int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM recall_embed_eligible WHERE id=?", d.id).Scan(&eligible); err != nil {
				return err
			}
			if eligible == 0 {
				continue
			}
			if d.kind == "summary" && strings.HasPrefix(d.source, "messenger-window:") {
				d.window = true
				continue
			}
			src, err := readSource(ctx, tx, d.kind, d.source)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			d.text, d.external = src.body.String, !src.body.Valid
			if d.kind != "event" {
				continue
			}
			spans, err := tx.QueryContext(ctx, "SELECT start,end,sensitivity FROM ev_span WHERE event_id=? AND sensitivity='secret'", d.source)
			if err != nil {
				return err
			}
			for spans.Next() {
				var span secrets.Span
				if err = spans.Scan(&span.Start, &span.End, &span.Sensitivity); err != nil {
					spans.Close()
					return err
				}
				d.spans = append(d.spans, span)
			}
			if err = errors.Join(spans.Err(), spans.Close()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		v.after = 0
		err = v.db.Write(ctx, func(tx *canon.Tx) error {
			current, _, err := v.generation(ctx, tx)
			if err != nil || current != generation {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE search_index_config SET ready=1 WHERE name='recall' AND NOT EXISTS(SELECT 1 FROM recall_embed_pending) AND NOT EXISTS(SELECT 1 FROM recall_pending) AND NOT EXISTS(SELECT 1 FROM messenger_window_pending)")
			return err
		})
		return 0, err
	}
	v.after = batch[len(batch)-1].sequence
	var docs []embeddingDocument
	var texts []string
	var skipped []embeddingDocument
	var failures error
	for _, d := range batch {
		if d.window {
			d.text, err = windowText(ctx, v.db, v.readEvent, d.source)
		} else if d.external {
			if v.readEvent == nil {
				failures = errors.Join(failures, ErrBytesUnavailable)
				continue
			}
			d.text, err = v.readEvent(ctx, d.source)
		} else {
			var masked secrets.Masked
			masked, err = secrets.Mask(d.text, d.spans...)
			d.text = masked.DerivedText()
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if strings.TrimSpace(d.text) == "" {
			skipped = append(skipped, d)
			continue
		}
		// The derived bytes must be exactly the bytes whose postings were published.
		if textHash(d.text) != d.hash {
			failures = errors.Join(failures, ErrBytesUnavailable)
			continue
		}
		docs = append(docs, d)
		texts = append(texts, d.text)
	}
	// Recheck after material reads and masking, immediately before dispatch.
	// An invalidated snapshot must never be sent to the endpoint.
	err = v.db.Read(ctx, func(tx *canon.Tx) error {
		current, _, err := v.generation(ctx, tx)
		if err != nil {
			return err
		}
		if current != generation {
			return embedding.ErrUnavailable
		}
		kept := docs[:0]
		texts = texts[:0]
		for _, d := range docs {
			var present int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recall_embed_pending q JOIN recall_embed_eligible d ON d.id=q.doc WHERE q.sequence=? AND d.id=? AND d.text_hash=?`, d.sequence, d.id, d.hash).Scan(&present); err != nil {
				return err
			}
			if present == 1 {
				kept = append(kept, d)
				texts = append(texts, d.text)
			}
		}
		docs = kept
		return nil
	})
	if err != nil {
		return 0, err
	}
	var vectors [][]float32
	if len(texts) > 0 {
		vectors, err = v.adapter.Embed(ctx, texts)
		if err != nil {
			return 0, err
		}
		if len(vectors) != len(docs) {
			return 0, embedding.ErrUnavailable
		}
		for _, vec := range vectors {
			if !validVector(vec, v.adapter.Config().Defaults().Dimension) {
				return 0, embedding.ErrUnavailable
			}
		}
	}
	completed := 0
	err = v.db.Write(ctx, func(tx *canon.Tx) error {
		current, _, err := v.generation(ctx, tx)
		if err != nil {
			return err
		}
		if current != generation {
			return nil
		}
		for i, d := range append(docs, skipped...) {
			var present int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recall_embed_pending q JOIN recall_doc d ON d.id=q.doc WHERE q.sequence=? AND d.id=? AND d.text_hash=?`, d.sequence, d.id, d.hash).Scan(&present); err != nil {
				return err
			}
			if present == 0 {
				continue
			}
			if i < len(docs) {
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM recall_embed_eligible WHERE id=?", d.id).Scan(&present); err != nil {
					return err
				}
				if present == 0 {
					continue
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO recall_vec(rowid,embedding) VALUES(?,?)", d.id, vectorBytes(vectors[i])); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM recall_embed_pending WHERE sequence=?", d.sequence); err != nil {
				return err
			}
			completed++
		}
		return nil
	})
	return completed, errors.Join(failures, err)
}

func validVector(vector []float32, dimension int) bool {
	if len(vector) != dimension {
		return false
	}
	var norm float64
	for _, n := range vector {
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return false
		}
		norm += float64(n) * float64(n)
	}
	return norm > 0
}
func vectorBytes(vector []float32) []byte {
	raw := make([]byte, len(vector)*4)
	for i, n := range vector {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(n))
	}
	return raw
}

// Query embeds only the masked question within the recall cap. Scope is applied
// before distance and limit; a global nearest-neighbor list could hide every
// permitted document behind another person's or agent's closer documents.
func (v *Vectors) Query(ctx context.Context, scope Scope, text string) ([]string, error) {
	if v == nil || v.adapter == nil {
		return nil, nil
	}
	if validScope(scope) != nil {
		return nil, ErrInvalid
	}
	var generation int64
	err := v.db.Read(ctx, func(tx *canon.Tx) error {
		var ready bool
		var err error
		generation, ready, err = v.generation(ctx, tx)
		if err == nil && !ready {
			err = embedding.ErrUnavailable
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	vectors, err := v.adapter.Embed(queryCtx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || !validVector(vectors[0], v.adapter.Config().Defaults().Dimension) {
		return nil, embedding.ErrUnavailable
	}
	at := scope.At
	if at == 0 {
		at = time.Now().Unix()
	}
	restricted := scope.Projection || scope.ExternalModel
	where, args := scopeWhere(scope, kindsOf(scope), at, restricted, "d")
	prefix, prefixArgs := restrictedCTE(scope)
	if !scope.History {
		older, olderArgs := olderExpr(kindsOf(scope), at, "d")
		where = append(where, "("+older+")=0")
		args = append(args, olderArgs...)
	}
	candidateSQL := `SELECT d.*,vec_distance_cosine(v.embedding,?) AS distance FROM recall_embed_eligible d JOIN recall_vec v ON v.rowid=d.id WHERE ` + strings.Join(where, " AND ")
	candidateArgs := append([]any{vectorBytes(vectors[0])}, args...)
	if scope.Origin == "messenger" && containsKind(kindsOf(scope), Event) {
		// A window is navigation: rank its in-scope records, never summary text.
		candidateSQL += ` UNION ALL SELECT d.*,vec_distance_cosine(v.embedding,?) AS distance FROM messenger_window w JOIN recall_embed_eligible wd ON wd.kind='summary' AND wd.source_id=w.id JOIN recall_vec v ON v.rowid=wd.id JOIN cite c ON c.view_kind='summary' AND c.view_id=w.id AND c.dropped_revision IS NULL JOIN recall_doc d ON d.kind='event' AND d.source_id=c.target_id WHERE ` + strings.Join(where, " AND ")
		candidateArgs = append(candidateArgs, vectorBytes(vectors[0]))
		candidateArgs = append(candidateArgs, args...)
	}
	prefix += `, candidates AS MATERIALIZED (` + candidateSQL + `), ` + lineageCTE(containsKind(kindsOf(scope), Event)) + `
 unique_docs AS (SELECT c.*,row_number() OVER(PARTITION BY CASE WHEN kind='event' THEN conversation||':'||text_hash ELSE kind||':'||source_id END ORDER BY distance,julianday(occurred_at),source_id) AS twin FROM candidates c` + lineageFilter(containsKind(kindsOf(scope), Event)) + `) SELECT kind,source_id FROM unique_docs WHERE twin=1 ORDER BY distance,julianday(occurred_at) DESC,source_id LIMIT ?`
	args = append(append(prefixArgs, candidateArgs...), VectorCandidates)
	var ids []string
	err = v.db.Read(ctx, func(tx *canon.Tx) error {
		current, ready, err := v.generation(ctx, tx)
		if err != nil {
			return err
		}
		if !ready || current != generation {
			return embedding.ErrUnavailable
		}
		rows, err := tx.QueryContext(ctx, prefix, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, id string
			if err := rows.Scan(&kind, &id); err != nil {
				return err
			}
			ids = append(ids, kind+"\x00"+id)
		}
		return rows.Err()
	})
	return ids, err
}
func restrictedCTE(scope Scope) (string, []any) {
	return `WITH restricted AS (
 SELECT DISTINCT c.target_id AS id FROM cite c JOIN memory_item m ON m.id=c.view_id
 WHERE c.view_kind='memory' AND c.target_kind='event' AND c.dropped_revision IS NULL AND m.person=?
 AND ((?=1 AND m.scope IN ('private','never-leaves')) OR (?=1 AND m.scope='never-leaves')))`, []any{scope.Person, boolInt(scope.Projection), boolInt(scope.ExternalModel)}
}

// SetImportConsent is called only after the import flow has presented its
// volume and obtained explicit consent. The row authorizes one conversation.
func SetImportConsent(ctx context.Context, db Canon, conversation string, enabled bool) error {
	if strings.TrimSpace(conversation) == "" {
		return ErrInvalid
	}
	return db.Write(ctx, func(tx *canon.Tx) error {
		if !enabled {
			_, err := tx.ExecContext(ctx, "DELETE FROM recall_embed_consent WHERE conversation=?", conversation)
			return err
		}
		var imported int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM recall_imported_event WHERE conversation_id=?", conversation).Scan(&imported); err != nil {
			return err
		}
		if imported == 0 {
			return ErrInvalid
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO recall_embed_consent SELECT ?,max(rowid),? FROM conv_event WHERE conversation_id=? ON CONFLICT(conversation) DO UPDATE SET through_event=excluded.through_event,granted_at=excluded.granted_at", conversation, time.Now().UTC().Format(time.RFC3339Nano), conversation)
		return err
	})
}
