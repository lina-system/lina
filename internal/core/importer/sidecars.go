// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
)

// Sidecar refers only to bytes already captured by SnapshotSidecar. A missing
// snapshot becomes an explicit unavailable material; wrong bytes are an error.
type Sidecar struct {
	Key, Event           LegacyKey
	SnapshotPath, SHA256 string
}

func (b *profileBuilder) addSidecar(ctx context.Context, s Sidecar) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e := b.records[s.Event]
	if e == nil || e.event == nil || s.Key.System != string(HermesLCM) || s.Event.System != string(HermesLCM) || s.Key.Instance != b.profile.ID || s.Key.Kind != "sidecars" || s.Key.Key == "" {
		return ErrSourceMissing
	}
	hash, err := hex.DecodeString(s.SHA256)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != s.SHA256 {
		return ErrSnapshot
	}
	data, err := os.ReadFile(s.SnapshotPath)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if !missing {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != s.SHA256 {
			return ErrSnapshot
		}
	}
	row := Row{}
	row["sha256"], _ = value(s.SHA256)
	row["bytes"], _ = value(data)
	if missing {
		row["bytes"], _ = value(nil)
	}
	r := b.add(b.byID(e.conversation), "material", 5, sourceRow{key: s.Key, row: row})
	r.links = []importLink{{e.id, "output", 0}}
	if missing {
		r.flags = append(r.flags, "bytes_unavailable")
	}
	if len(ScanSecrets(data)) > 0 {
		r.flags = append(r.flags, "secret")
	}
	found := false
	for i := range b.p.inventory {
		c := &b.p.inventory[i]
		if c.System == s.Key.System && c.Instance == s.Key.Instance && c.Table == s.Key.Kind {
			if c.Omitted != "" {
				return ErrSnapshot
			}
			c.Rows++
			found = true
			break
		}
	}
	if !found {
		b.p.inventory = append(b.p.inventory, TableCount{System: s.Key.System, Instance: s.Key.Instance, Table: s.Key.Kind, Rows: 1})
	}
	return nil
}
