// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (s Source) Episodes(ctx context.Context, scope Scope, q Selection) ([]Episode, Manifest, error) {
	manifest := Manifest{Version: 1, Reasons: map[string]int{}}
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return nil, manifest, err
	}
	manifest.RevocationEpoch = epoch
	ids, err := s.Select(ctx, scope, q)
	if err != nil {
		return nil, manifest, err
	}
	manifest.Selected = len(ids)
	episodes := []Episode{}
	for _, id := range ids {
		ep, err := s.Load(ctx, scope, id)
		if err != nil {
			var exclusion Exclusion
			if !errors.As(err, &exclusion) {
				return nil, manifest, err
			}
			manifest.LeftOut++
			manifest.Reasons[string(exclusion)]++
			continue
		}
		episodes = append(episodes, ep)
		manifest.Exported++
	}
	if err = s.Validate(ctx, epoch); err != nil {
		return nil, manifest, err
	}
	return episodes, manifest, nil
}

// StageExport publishes a private directory only after all source checks pass.
// It writes only staging/export; no canon or conversation write API is available.
func (s Source) StageExport(ctx context.Context, root string, scope Scope, q Selection) (string, Manifest, error) {
	episodes, manifest, err := s.Episodes(ctx, scope, q)
	if err != nil {
		return "", manifest, err
	}
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for _, ep := range episodes {
		if err = enc.Encode(ep); err != nil {
			return "", manifest, err
		}
	}
	metadata, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", manifest, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", manifest, err
	}
	defer dir.Close()
	if err = dir.MkdirAll("staging/export", 0700); err != nil {
		return "", manifest, err
	}
	id := rand.Text()
	temporary := filepath.Join("staging/export", ".episode-"+id)
	published := filepath.Join("staging/export", "episode-"+id)
	if err = dir.Mkdir(temporary, 0700); err != nil {
		return "", manifest, err
	}
	defer dir.RemoveAll(temporary)
	for name, body := range map[string][]byte{"episodes.jsonl": data.Bytes(), "manifest.json": append(metadata, '\n')} {
		file, err := dir.OpenFile(filepath.Join(temporary, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", manifest, err
		}
		_, writeErr := file.Write(body)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
			return "", manifest, err
		}
	}
	for _, ep := range episodes {
		if err = s.ValidateEpisode(ctx, ep); err != nil {
			return "", manifest, err
		}
	}
	if err = s.Validate(ctx, manifest.RevocationEpoch); err != nil {
		return "", manifest, err
	}
	if err = dir.Rename(temporary, published); err != nil {
		return "", manifest, err
	}
	return filepath.Join(root, published), manifest, nil
}
