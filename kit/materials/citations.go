// SPDX-License-Identifier: Apache-2.0

// Package materials checks revision-pinned passage citations without owning storage.
package materials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// Anchor identifies a half-open byte span in the captured revision. Page and
// Section are display locators; URL and CapturedAt retain source provenance.
// Identity and resolution always use the asset, revision, span and full hash.
type Anchor struct {
	AssetID    string `json:"asset_id"`
	Revision   int64  `json:"revision"`
	Start, End int64
	Page       int    `json:"page,omitempty"`
	Section    string `json:"section,omitempty"`
	URL        string `json:"url,omitempty"`
	CapturedAt int64  `json:"captured_at,omitempty"`
	Hash       string `json:"hash"`
}
type Reader interface {
	OpenRead(context.Context, string, int64) (io.ReadCloser, error)
}
type Citation struct {
	Anchor   Anchor `json:"anchor"`
	Resolved bool   `json:"resolved"`
	Reason   string `json:"reason,omitempty"`
}

// Check consumes each revision to EOF, allowing the store to verify its own
// bytes too. Missing, deleted, corrupt and invalid evidence stays unresolved.
// It buffers no document and never substitutes the current revision.
func Check(ctx context.Context, source Reader, anchors []Anchor) ([]Citation, error) {
	out := make([]Citation, 0, len(anchors))
	for _, a := range anchors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := Citation{Anchor: a, Reason: "invalid_anchor"}
		digest, err := hex.DecodeString(a.Hash)
		if a.AssetID != "" && a.Revision > 0 && a.Start >= 0 && a.End > a.Start && len(digest) == sha256.Size && err == nil && source != nil {
			c.Reason = "unavailable_evidence"
			reader, err := source.OpenRead(ctx, a.AssetID, a.Revision)
			if err == nil {
				h := sha256.New()
				n, e := io.Copy(h, reader)
				e = errors.Join(e, reader.Close())
				if e == nil {
					c.Reason = "changed_evidence"
					if hex.EncodeToString(h.Sum(nil)) == a.Hash && a.End <= n {
						c.Resolved = true
						c.Reason = ""
					}
				}
			}
		}
		out = append(out, c)
	}
	return out, ctx.Err()
}
