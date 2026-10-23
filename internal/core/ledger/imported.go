// SPDX-License-Identifier: Apache-2.0
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// Imported output assets retain original bytes. Apply their original source
// spans before the ordinary native-display validation, never treating an
// unmasked material as ready for indexing or model evidence.
func (s *Store) importedOutput(ctx context.Context, id, text string) (secrets.Masked, bool, error) {
	var spans []secrets.Span
	var missing int
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM import_link l JOIN import_record r ON r.id=l.record_id WHERE l.target_id=? AND l.kind='output' AND EXISTS(SELECT 1 FROM json_each(r.flags) WHERE value='bytes_unavailable')`, id).Scan(&missing); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT s.start,s.end,s.sensitivity FROM import_source_span s JOIN legacy_ref ref ON ref.system=s.system AND ref.instance=s.instance AND ref.kind=s.kind AND ref.key=s.key JOIN import_link l ON l.record_id=ref.record_id JOIN conv_event e ON e.id=l.target_id WHERE l.target_id=? AND l.kind='output' AND s.column_name='bytes' AND e.material_id IS NOT NULL ORDER BY s.start,s.end`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var span secrets.Span
			if err = rows.Scan(&span.Start, &span.End, &span.Sensitivity); err != nil {
				rows.Close()
				return err
			}
			spans = append(spans, span)
		}
		return errors.Join(rows.Err(), rows.Close())
	})
	if err != nil {
		return secrets.Masked{}, false, err
	}
	if missing != 0 {
		return secrets.Masked{}, true, nil
	}
	// Match import publication: original detector spans can cross JSON syntax.
	// Inline fallbacks have no material and must never use original offsets.
	if json.Valid([]byte(text)) {
		spans = nil
	}
	masked, err := secrets.Mask(text, spans...)
	return masked, false, err
}
