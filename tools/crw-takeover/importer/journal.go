// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/records"
)

type reservation struct {
	ID        string `json:"id"`
	Published bool   `json:"published"`
}
type journal struct {
	Version int                    `json:"version"`
	SHA256  string                 `json:"sha256"`
	Actor   records.Actor          `json:"actor"`
	Entries map[string]reservation `json:"entries"`
}

func loadJournal(name, hash string, actor records.Actor) (journal, error) {
	j := journal{Version: 1, SHA256: hash, Actor: actor, Entries: map[string]reservation{}}
	data, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return j, err
	}
	if err = decode(data, &j); err != nil {
		return j, err
	}
	if j.Version != 1 || j.SHA256 != hash || j.Actor != actor || j.Entries == nil {
		return j, errors.New("journal belongs to a different snapshot or actor")
	}
	ids := map[string]bool{}
	for _, entry := range j.Entries {
		if entry.ID == "" || ids[entry.ID] {
			return j, errors.New("invalid journal identity")
		}
		ids[entry.ID] = true
	}
	return j, nil
}

// SaveJSON uses private, synced replacement. The caller holds the Core state
// root lock throughout reservation, publication, reconciliation and reporting.
func SaveJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(name)
	f, err := os.CreateTemp(dir, ".takeover-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, name); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
