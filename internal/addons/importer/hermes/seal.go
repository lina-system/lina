// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

type BodyFailure struct {
	EventID string
	Reason  string
}

type SealReport struct {
	AddedSeals        int
	Verification      Verification
	BackupGeneration  string
	UnavailableBodies int
	BodyFailures      []BodyFailure
}

// Seal publishes only a verified range. A failed backup leaves an explicit
// incomplete seal which a retry resumes; promotion remains unavailable. The
// callback is the host's Core backup.Create while it holds the state-root lock.
func (p *Plan) Seal(ctx context.Context, db Canon, backup func(context.Context) (string, error)) (SealReport, error) {
	report := SealReport{}
	if backup == nil {
		return report, ErrSnapshot
	}
	needBackup := false
	err := db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		report.Verification, err = p.verifyTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, c := range p.conversations {
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM import_seal WHERE conversation_id=?", c.record.id).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				if err = p.publishConversation(ctx, tx, c); err != nil {
					return err
				}
				report.AddedSeals++
			}
			var generation string
			if err = tx.QueryRowContext(ctx, "SELECT backup_generation FROM import_seal WHERE conversation_id=?", c.record.id).Scan(&generation); err != nil {
				return err
			}
			if generation == "" {
				needBackup = true
			} else {
				report.BackupGeneration = generation
			}
			// Read the durable outcome on retries too; counts describe the range,
			// not just the events newly published by this call.
			rows, err := tx.QueryContext(ctx, `SELECT id,json_extract(origin,'$.body_masking_error') FROM conv_event
 WHERE conversation_id=? AND json_extract(origin,'$.body_masking_error') IS NOT NULL ORDER BY id`, c.record.id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var failure BodyFailure
				if err := rows.Scan(&failure.EventID, &failure.Reason); err != nil {
					rows.Close()
					return err
				}
				report.BodyFailures = append(report.BodyFailures, failure)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
		}
		report.UnavailableBodies = len(report.BodyFailures)
		return nil
	})
	if err != nil {
		return report, err
	}
	if !needBackup {
		return report, nil
	}
	generation, err := backup(ctx)
	if err != nil {
		return report, err
	}
	if generation == "" {
		return report, ErrSnapshot
	}
	err = db.Write(ctx, func(tx *canon.Tx) error {
		for _, c := range p.conversations {
			if _, err := tx.ExecContext(ctx, "UPDATE import_seal SET backup_generation=? WHERE conversation_id=? AND backup_generation=''", generation, c.record.id); err != nil {
				return err
			}
		}
		return nil
	})
	report.BackupGeneration = generation
	return report, err
}

func (p *Plan) publishConversation(ctx context.Context, tx *canon.Tx, c *conversation) error {
	parent := ""
	for _, r := range c.records {
		if r.event == nil {
			continue
		}
		e := r.event
		var material sql.NullString
		var materialRevision sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT material_id,material_revision FROM import_event WHERE id=?", r.id).Scan(&material, &materialRevision); err != nil {
			return err
		}
		display := r.body
		if material.Valid {
			found := false
			for _, attachment := range c.records {
				if attachment.kind == "material" && len(attachment.links) == 1 && attachment.links[0].target == r.id {
					display = attachment.sources[0].row.text("bytes")
					found = true
					break
				}
			}
			if !found {
				return ErrConflict
			}
		}
		// Originals and byte-addressed flags stay in import_event for lossless
		// reverse mapping; live consumers use the native masked representation.
		var spans []secrets.Span
		// Raw assignment spans may include JSON's quotes or escape sequences.
		// Let the shared JSON scanner inspect decoded strings instead. These
		// spans are detector output, not independent sensitivity annotations.
		if !json.Valid([]byte(display)) {
			for _, s := range ScanSecrets([]byte(display)) {
				spans = append(spans, secrets.Span{Start: s.Start, End: s.End, Sensitivity: s.Kind})
			}
		}
		masked, err := secrets.Mask(display, spans...)
		if err == nil && json.Valid([]byte(display)) {
			// The decoded-string scanner must not weaken the original scan.
			// For example, a numeric token value has no string boundary it can
			// mask. Retain only placeholder matches; otherwise use the fallback.
			if !json.Valid([]byte(masked.Text())) {
				err = secrets.ErrSpan
			}
			for _, s := range ScanSecrets([]byte(masked.Text())) {
				if strings.Trim(masked.Text()[s.Start:s.End], "\"'") != secret.Placeholder {
					err = secrets.ErrSpan
					break
				}
			}
		}
		originFields := map[string]string{"kind": "import", "locator": r.id, "source_session": e.session}
		if errors.Is(err, secrets.ErrSpan) {
			// Keep the original in import_event / its material. Publish only a
			// safe inline substitute so one bad body cannot refuse the profile.
			originFields["body_masking_error"] = secrets.ErrSpan.Error()
			fallback := "Imported body unavailable (" + secrets.ErrSpan.Error() + "): "
			masked, err = secrets.Mask(fallback+secret.Placeholder, secrets.Span{Start: len(fallback), End: len(fallback) + len(secret.Placeholder), Sensitivity: "secret"})
			material = sql.NullString{}
			materialRevision = sql.NullInt64{}
		}
		if err != nil {
			return err
		}
		var body any = masked.Text()
		if material.Valid {
			body = nil
		}
		origin, _ := json.Marshal(originFields)
		_, err = tx.ExecContext(ctx, `INSERT INTO conv_event(id,conversation_id,parent_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,replay_of,body,material_id,material_revision)
   VALUES(?,?,?,?,?,?,'{"kind":"import"}',?,?,?,?,?,?,?,?)`, r.id, r.conversation, nullable(e.parent), nullable(e.classified.person), r.agent, e.classified.kind, e.occurred, e.recorded, e.quality, string(origin), nullable(e.replay), body, material, materialRevision)
		if err != nil {
			return err
		}
		for _, s := range masked.Spans() {
			if _, err = tx.ExecContext(ctx, "INSERT INTO ev_span(event_id,start,end,sensitivity) VALUES(?,?,?,?)", r.id, s.Start, s.End, s.Sensitivity); err != nil {
				return err
			}
		}
		parent = r.id
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ev_rel(event_id,target_id,kind,ordinal)
 SELECT r.event_id,r.target_id,r.kind,r.ordinal FROM import_event_relation r JOIN import_event e ON e.id=r.event_id WHERE e.conversation_id=?`, c.record.id); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sealID := opaque(c.record.id, "import_sealed")
	body, _ := json.Marshal(map[string]string{"type": "import_sealed", "manifest": manifest(c)})
	if _, err := tx.ExecContext(ctx, `INSERT INTO conv_event(id,conversation_id,parent_id,agent_id,speaker_kind,surface,scaffold_kind,occurred_at,recorded_at,time_quality,origin,body)
 VALUES(?,?,?,?,'system','{"kind":"import"}','import_sealed',?,?,'recorded','{"kind":"import"}',?)`, sealID, c.record.id, nullable(parent), c.record.agent, now, now, string(body)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO import_seal(conversation_id,event_id) VALUES(?,?)", c.record.id, sealID)
	return err
}

// Finish takes the post-promotion generation too. It deliberately reports a
// backup error even when durable promotion receipts allow an idempotent retry.
func (p *Plan) Finish(ctx context.Context, db Canon, policy PromotionPolicy, accepted []string, confirmation string, backup func(context.Context) (string, error)) (SealReport, int, error) {
	seal, err := p.Seal(ctx, db, backup)
	if err != nil {
		return seal, 0, err
	}
	if len(accepted) == 0 {
		return seal, 0, nil
	}
	n, err := p.Promote(ctx, db, policy, accepted, confirmation)
	if err != nil {
		return seal, 0, err
	}
	// Persist completion separately from individual receipts: a crash after
	// promotion but before backup must retry the backup, even when n is zero.
	pending := false
	err = db.Read(ctx, func(tx *canon.Tx) error {
		for _, id := range accepted {
			var generation string
			if err := tx.QueryRowContext(ctx, "SELECT backup_generation FROM import_promotion WHERE cluster_id=?", id).Scan(&generation); err != nil {
				return err
			}
			pending = pending || generation == ""
			if generation != "" {
				seal.BackupGeneration = generation
			}
		}
		return nil
	})
	if err != nil {
		return seal, n, err
	}
	if pending {
		generation, err := backup(ctx)
		if err != nil {
			return seal, n, err
		}
		if generation == "" {
			return seal, n, ErrSnapshot
		}
		err = db.Write(ctx, func(tx *canon.Tx) error {
			for _, id := range accepted {
				if _, err := tx.ExecContext(ctx, "UPDATE import_promotion SET backup_generation=? WHERE cluster_id=? AND backup_generation=''", generation, id); err != nil {
					return err
				}
			}
			return nil
		})
		seal.BackupGeneration = generation
		return seal, n, err
	}
	return seal, n, nil
}
