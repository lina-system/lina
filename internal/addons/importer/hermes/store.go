// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
)

type Reference struct {
	Key                                       LegacyKey
	RecordID, SourceHash, BatchID, ImportedAt string
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func manifest(c *conversation) string {
	var records []any
	for _, r := range c.records {
		var sources, links []any
		for _, s := range r.sources {
			sources = append(sources, []any{s.key, RowHash(s.row), s.bodyColumn})
		}
		for _, l := range r.links {
			links = append(links, []any{l.target, l.kind, l.ordinal})
		}
		var e any
		if r.event != nil {
			v := r.event
			e = []any{v.session, v.role, v.parent, v.replay, v.occurred, v.recorded, v.quality, v.classified}
		}
		records = append(records, []any{r.id, r.agent, r.kind, r.body, r.stage, r.flags, sources, links, e})
	}
	return digest([]any{records, c.report, c.hidden})
}

// Apply writes one complete conversation, including every stage and reference,
// inside the caller's fenced transaction. It can be registered as a Core job
// Apply callback; the deterministic worker needs no model or writable database.
func (p *Plan) Apply(ctx context.Context, tx *canon.Tx, index int) (Report, error) {
	if index < 0 || index >= len(p.conversations) {
		return Report{}, ErrSnapshot
	}
	c := p.conversations[index]
	hash := manifest(c)
	var existing string
	err := tx.QueryRowContext(ctx, "SELECT manifest_hash FROM import_conversation WHERE id=?", c.record.id).Scan(&existing)
	if err == nil {
		if existing != hash {
			return Report{}, ErrConflict
		}
		return Report{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Report{}, err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	rehearsal, err := p.Rehearse()
	if err != nil {
		return Report{}, err
	}
	encodedReport, _ := json.Marshal(rehearsal)
	if _, err := tx.ExecContext(ctx, "INSERT INTO import_batch(id,created_at) VALUES(?,?) ON CONFLICT(id) DO NOTHING", p.batch, at); err != nil {
		return Report{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO import_rehearsal(batch_id,report) VALUES(?,?) ON CONFLICT(batch_id) DO NOTHING", p.batch, string(encodedReport)); err != nil {
		return Report{}, err
	}
	report := Report{}
	materials := map[string]assets.Asset{}
	for _, r := range c.records {
		if r.material != nil {
			a, err := r.material.Insert(ctx, tx)
			if err != nil {
				return Report{}, err
			}
			materials[r.links[0].target] = a
		}
	}
	for _, r := range c.records {
		if r.event != nil {
			if err := writeImportedEvent(ctx, tx, r, materials[r.id]); err != nil {
				return Report{}, err
			}
			report.AddedEvents++
		}
		flags, _ := json.Marshal(r.flags)
		var eventID any
		body := r.body
		if r.event != nil {
			eventID = r.id
			body = "" // the event owns its canonical body
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO import_record(id,conversation_id,agent_id,kind,stage,body,flags,event_id) VALUES(?,?,?,?,?,?,?,?)", r.id, r.conversation, r.agent, r.kind, r.stage, body, string(flags), eventID); err != nil {
			return Report{}, err
		}
		report.AddedRecords++
		for _, s := range r.sources {
			fields := s.row.clone()
			if s.bodyColumn != "" {
				delete(fields, s.bodyColumn)
			}
			encoded, _ := json.Marshal(fields)
			if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_ref(system,instance,kind,key,record_id,source_hash,batch_id,imported_at,fields,body_column) VALUES(?,?,?,?,?,?,?,?,?,?)`, s.key.System, s.key.Instance, s.key.Kind, s.key.Key, r.id, RowHash(s.row), p.batch, at, string(encoded), s.bodyColumn); err != nil {
				return Report{}, fmt.Errorf("%w: legacy reference: %v", ErrConflict, err)
			}
			for column, v := range s.row {
				if v.Kind != "text" && v.Kind != "blob" {
					continue
				}
				for _, span := range ScanSecrets([]byte(s.row.text(column))) {
					if _, err := tx.ExecContext(ctx, "INSERT INTO import_source_span(system,instance,kind,key,column_name,start,end,sensitivity) VALUES(?,?,?,?,?,?,?,?)", s.key.System, s.key.Instance, s.key.Kind, s.key.Key, column, span.Start, span.End, span.Kind); err != nil {
						return Report{}, err
					}
				}
			}
			report.AddedReferences++
		}
	}
	// All destinations now exist, including forward summary edges.
	for _, r := range c.records {
		for _, l := range r.links {
			if _, err := tx.ExecContext(ctx, "INSERT INTO import_link(record_id,target_id,kind,ordinal) VALUES(?,?,?,?)", r.id, l.target, l.kind, l.ordinal); err != nil {
				return Report{}, err
			}
		}
		if r.event != nil {
			for _, l := range r.links {
				if l.kind == "supersedes" || l.kind == "view_of" {
					if err := writeRelation(ctx, tx, r.id, l.target, l.kind, l.ordinal); err != nil {
						return Report{}, err
					}
				}
			}
		}
		if r.kind == "relation" && len(r.links) == 2 {
			a, z := r.links[0], r.links[1]
			if err := writeRelation(ctx, tx, a.target, z.target, z.kind, z.ordinal); err != nil {
				return Report{}, err
			}
		}
	}
	encoded, _ := json.Marshal(c.report)
	if _, err := tx.ExecContext(ctx, "INSERT INTO import_conversation(id,batch_id,manifest_hash,speaker_report,hidden) VALUES(?,?,?,?,?)", c.record.id, p.batch, hash, string(encoded), c.hidden); err != nil {
		return Report{}, err
	}
	return report, nil
}
func writeImportedEvent(ctx context.Context, tx *canon.Tx, r *record, material assets.Asset) error {
	e := r.event
	c := e.classified
	origin, _ := json.Marshal(map[string]string{"kind": "import", "source_session": e.session})
	surface := `{"kind":"import"}`
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_event(id,conversation_id,parent_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,replay_of,body,material_id,material_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.id, r.conversation, nullable(e.parent), nullable(c.person), r.agent, c.kind, surface, e.occurred, e.recorded, e.quality, string(origin), nullable(e.replay), r.body, nullable(material.ID), materialRevision(material)); err != nil {
		return err
	}
	for _, span := range ScanSecrets([]byte(r.body)) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO import_event_span(event_id,start,end,sensitivity) VALUES(?,?,?,?)", r.id, span.Start, span.End, span.Kind); err != nil {
			return err
		}
	}
	return nil
}
func resolve(ctx context.Context, tx *canon.Tx, key LegacyKey) (Reference, error) {
	r := Reference{Key: key}
	err := tx.QueryRowContext(ctx, "SELECT record_id,source_hash,batch_id,imported_at FROM legacy_ref WHERE system=? AND instance=? AND kind=? AND key=?", key.System, key.Instance, key.Kind, key.Key).Scan(&r.RecordID, &r.SourceHash, &r.BatchID, &r.ImportedAt)
	return r, err
}
func Resolve(ctx context.Context, db Canon, key LegacyKey) (Reference, error) {
	var r Reference
	err := db.Read(ctx, func(tx *canon.Tx) error { var err error; r, err = resolve(ctx, tx, key); return err })
	return r, err
}
func References(ctx context.Context, db Canon, id string) ([]Reference, error) {
	var refs []Reference
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT system,instance,kind,key,source_hash,batch_id,imported_at FROM legacy_ref WHERE record_id=? ORDER BY system,instance,kind,key", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := Reference{RecordID: id}
			if err := rows.Scan(&r.Key.System, &r.Key.Instance, &r.Key.Kind, &r.Key.Key, &r.SourceHash, &r.BatchID, &r.ImportedAt); err != nil {
				return err
			}
			refs = append(refs, r)
		}
		return rows.Err()
	})
	return refs, err
}
func reverse(ctx context.Context, tx *canon.Tx, key LegacyKey) (Row, error) {
	var fields, column, body string
	err := tx.QueryRowContext(ctx, `SELECT l.fields,l.body_column,COALESCE(e.body,r.body) FROM legacy_ref l JOIN import_record r ON r.id=l.record_id LEFT JOIN import_event e ON e.id=r.event_id WHERE l.system=? AND l.instance=? AND l.kind=? AND l.key=?`, key.System, key.Instance, key.Kind, key.Key).Scan(&fields, &column, &body)
	if err != nil {
		return nil, err
	}
	var row Row
	if err = json.Unmarshal([]byte(fields), &row); err != nil || row == nil {
		return nil, ErrSnapshot
	}
	if column != "" {
		v, _ := value(body)
		row[column] = v
	}
	if err = checkRow(row); err != nil {
		return nil, err
	}
	return row, nil
}

// Reverse reconstructs the complete typed source row from adopted fields and
// the canonical record's body. It never reads the source or a snapshot file.
func Reverse(ctx context.Context, db Canon, key LegacyKey) (Row, error) {
	var row Row
	err := db.Read(ctx, func(tx *canon.Tx) error { var err error; row, err = reverse(ctx, tx, key); return err })
	return row, err
}
func (p *Plan) Verify(ctx context.Context, db Canon) (Verification, error) {
	var result Verification
	err := db.Read(ctx, func(tx *canon.Tx) error { var err error; result, err = p.verifyTx(ctx, tx); return err })
	return result, err
}
func (p *Plan) verifyTx(ctx context.Context, tx *canon.Tx) (Verification, error) {
	result := Verification{}
	err := func() error {
		if err := p.verifyEdges(ctx, tx); err != nil {
			return err
		}
		for _, c := range p.conversations {
			if err := verifyInterpretation(ctx, tx, c); err != nil {
				return err
			}
			expected := 0
			for _, r := range c.records {
				for _, s := range r.sources {
					ref, err := resolve(ctx, tx, s.key)
					if err != nil {
						return err
					}
					if ref.RecordID != r.id || ref.SourceHash != RowHash(s.row) {
						return ErrConflict
					}
					row, err := reverse(ctx, tx, s.key)
					if err != nil {
						return err
					}
					if RowHash(row) != ref.SourceHash {
						return ErrConflict
					}
					expected++
					result.Rows++
				}
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM legacy_ref l JOIN import_record r ON r.id=l.record_id WHERE r.conversation_id=?", c.record.id).Scan(&count); err != nil {
				return err
			}
			if count != expected {
				return ErrConflict
			}
			var hash string
			if err := tx.QueryRowContext(ctx, "SELECT manifest_hash FROM import_conversation WHERE id=?", c.record.id).Scan(&hash); err != nil {
				return err
			}
			if hash != manifest(c) {
				return ErrConflict
			}
		}
		for _, table := range p.inventory {
			counts := Reconciliation{System: table.System, Instance: table.Instance, Table: table.Table, SnapshotRows: table.Rows, Omitted: table.Omitted}
			if err := tx.QueryRowContext(ctx, "SELECT count(*),count(DISTINCT l.record_id) FROM legacy_ref l JOIN import_record r ON r.id=l.record_id WHERE l.system=? AND l.instance=? AND l.kind=?", table.System, table.Instance, table.Table).Scan(&counts.MappedRows, &counts.DistinctRecords); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM legacy_ref WHERE system=? AND instance=? AND kind=?", table.System, table.Instance, table.Table).Scan(&counts.References); err != nil {
				return err
			}
			expected := table.Rows
			if table.Omitted != "" {
				expected = 0
			}
			if counts.MappedRows != expected || counts.References != expected {
				return ErrConflict
			}
			result.Tables = append(result.Tables, counts)
		}
		return nil
	}()
	return result, err
}

// Distinct source relation rows can describe the same canonical edge. Keep all
// their references while inserting the append-only ledger edge only once.
func writeRelation(ctx context.Context, tx *canon.Tx, from, to, kind string, ordinal int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO import_event_relation(event_id,target_id,kind,ordinal)
 SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM import_event_relation WHERE event_id=? AND target_id=? AND kind=? AND ordinal=?)`, from, to, kind, ordinal, from, to, kind, ordinal)
	return err
}

func materialRevision(a assets.Asset) any {
	if a.ID == "" {
		return nil
	}
	return a.Revision
}
