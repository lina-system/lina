// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type SealReport struct {
	AddedSeals       int
	Verification     Verification
	BackupGeneration string
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
		}
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
		// Originals and byte-addressed flags stay in import_event for lossless
		// reverse mapping; live consumers use the native masked representation.
		var spans []secrets.Span
		for _, s := range ScanSecrets([]byte(r.body)) {
			spans = append(spans, secrets.Span{Start: s.Start, End: s.End, Sensitivity: s.Kind})
		}
		masked, err := secrets.Mask(r.body, spans...)
		if err != nil {
			return err
		}
		origin, _ := json.Marshal(map[string]string{"kind": "import", "locator": r.id, "source_session": e.session})
		_, err = tx.ExecContext(ctx, `INSERT INTO conv_event(id,conversation_id,parent_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,replay_of,body)
   VALUES(?,?,?,?,?,?,'{"kind":"import"}',?,?,?,?,?,?)`, r.id, r.conversation, nullable(e.parent), nullable(e.classified.person), r.agent, e.classified.kind, e.occurred, e.recorded, e.quality, string(origin), nullable(e.replay), masked.Text())
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
