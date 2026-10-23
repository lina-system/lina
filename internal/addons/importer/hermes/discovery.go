// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Registered from LCM-X externalize.py at the revision in REFERENCE.md. Reads
// only basename references under the explicitly handed-over output directory.
var outputReference = regexp.MustCompile(`\[(?:Externalized|GC'd externalized) (?:tool output|payload|LCM ingest payload):[^\n]*?;\s*ref=([^;\]\s]+)\]`)

func DiscoverSidecars(ctx context.Context, sourceRoot, relativeDir, staging string, profiles []Profile) ([]Sidecar, error) {
	if !filepath.IsLocal(relativeDir) {
		return nil, invalidSnapshot("sidecar directory must be local")
	}
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var result []Sidecar
	for _, profile := range profiles {
		if profile.LCMPath == "" {
			continue
		}
		db, err := openSpeakerSource(ctx, profile.LCMPath, HermesLCM)
		if err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx, "SELECT store_id,content FROM messages ORDER BY store_id")
		if err != nil {
			db.Close()
			return nil, err
		}
		type output struct {
			id   int64
			body string
		}
		var outputs []output
		for rows.Next() {
			var out output
			var body *string
			if err = rows.Scan(&out.id, &body); err != nil {
				rows.Close()
				db.Close()
				return nil, err
			}
			if body != nil {
				out.body = *body
			}
			outputs = append(outputs, out)
		}
		err = errors.Join(rows.Err(), rows.Close(), db.Close())
		if err != nil {
			return nil, err
		}
		for _, out := range outputs {
			matches := outputReference.FindAllStringSubmatch(out.body, -1)
			names := map[string]bool{}
			for _, match := range matches {
				names[match[1]] = true
			}
			// A source message remains one record. The existing stage model has one
			// output material; refuse ambiguity instead of losing a second payload.
			if len(names) > 1 {
				return nil, fmt.Errorf("%w: multiple output sidecars for one message", ErrSnapshot)
			}
			for name := range names {
				if name == "." || name == ".." || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".json") {
					return nil, invalidSnapshot("sidecar reference basename")
				}
				key := strconv.FormatInt(out.id, 10)
				relative := filepath.Join(relativeDir, name)
				sidecar := Sidecar{Key: LegacyKey{string(HermesLCM), profile.ID, "sidecars", key + ":" + name}, Event: LegacyKey{string(HermesLCM), profile.ID, "messages", key}, SnapshotPath: filepath.Join(staging, opaque(profile.ID, key, name)+".json")}
				info, err := root.Lstat(relative)
				if errors.Is(err, os.ErrNotExist) {
					result = append(result, sidecar)
					continue
				}
				if err != nil {
					return nil, err
				}
				if !info.Mode().IsRegular() {
					return nil, invalidSnapshot("sidecar regular file")
				}
				data, err := root.ReadFile(relative)
				if err != nil {
					return nil, err
				}
				var payload struct {
					Kind    string  `json:"kind"`
					Content *string `json:"content"`
					Chars   *int    `json:"content_chars"`
					Bytes   *int    `json:"content_bytes"`
				}
				if json.Unmarshal(data, &payload) != nil || payload.Content == nil || payload.Chars == nil || payload.Bytes == nil || *payload.Chars != utf8.RuneCountInString(*payload.Content) || *payload.Bytes != len(*payload.Content) {
					return nil, ErrSchema
				}
				switch payload.Kind {
				case "", "tool_result", "raw_payload", "ingest_payload":
				default:
					return nil, ErrSchema
				}
				hash := sha256.Sum256(data)
				sidecar.SHA256 = hex.EncodeToString(hash[:])
				if err = SnapshotSidecar(ctx, sourceRoot, relative, sidecar.SnapshotPath, sidecar.SHA256); err != nil {
					return nil, err
				}
				result = append(result, sidecar)
			}
		}
	}
	return result, nil
}
