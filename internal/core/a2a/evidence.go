// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/secrets"
)

func (s *Service) reflectsCredential(c Connection, b []byte) bool {
	if c.Scheme == "none" {
		return false
	}
	found, err := s.Vault.Contains(key(c), b)
	return found || err != nil && !errors.Is(err, secrets.ErrNotFound)
}
func (s *Service) hasSecret(c Connection, b []byte) bool {
	if s.reflectsCredential(c, b) {
		return true
	}
	if !utf8.Valid(b) {
		return false
	}
	masked, err := secrets.Mask(string(b))
	return err != nil || masked.Text() != string(b)
}

func (s *Service) verify(ctx context.Context, c Connection, call *Call, artifacts []Artifact) error {
	call.Verified = false
	call.Validated = false
	call.VerifiedHash = ""
	call.State = "unverified"
	call.Evidence = nil
	if len(artifacts) == 0 || len(artifacts) > 32 {
		return s.save(ctx, *call)
	}
	total := 0
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		if s.reflectsCredential(c, []byte(artifact.ID)) || artifact.ID == "" || len(artifact.ID) > 1024 || seen[artifact.ID] || len(artifact.Parts) == 0 || len(artifact.Parts) > 32 {
			return ErrEvidence
		}
		seen[artifact.ID] = true
		var combined []byte
		mediaType := ""
		for _, part := range artifact.Parts {
			count := 0
			for _, yes := range []bool{part.Text != nil, part.Raw != "", part.URL != "", part.Data != nil} {
				if yes {
					count++
				}
			}
			if count != 1 {
				return ErrEvidence
			}
			media := part.MediaType
			if media == "" && part.Text != nil {
				media = "text/plain"
			}
			if mediaType != "" && mediaType != media {
				return ErrEvidence
			}
			mediaType = media
			if media != call.Profile.MediaType {
				return ErrEvidence
			}
			var body []byte
			var err error
			switch {
			case part.Text != nil:
				body = []byte(*part.Text)
			case part.Raw != "":
				body, err = base64.StdEncoding.DecodeString(part.Raw)
			case part.Data != nil:
				if !json.Valid(part.Data) {
					return ErrEvidence
				}
				body = part.Data
			case part.URL != "":
				response, e := s.request(ctx, call.Origin, c, call.Profile, "GET", part.URL, nil)
				if e != nil {
					return e
				}
				body, err = readBounded(response.Body, call.Profile.MaxOutput-total)
				response.Body.Close()
				// Retain the origin and content hash, not a URL which might carry tokens.
			}
			if err != nil {
				return ErrEvidence
			}
			total += len(body)
			if total > call.Profile.MaxOutput || (strings.HasPrefix(media, "text/") && !utf8.Valid(body)) || s.hasSecret(c, body) {
				return ErrEvidence
			}
			if media == "application/json" {
				var decoded any
				if json.Unmarshal(body, &decoded) != nil {
					return ErrEvidence
				}
				canonical, _ := json.Marshal(decoded)
				if s.hasSecret(c, canonical) {
					return ErrEvidence
				}
			}
			combined = append(combined, body...)
		}
		call.Evidence = append(call.Evidence, Evidence{Artifact: artifact.ID, MediaType: mediaType, Body: bytes.Clone(combined), SHA256: hash(combined), Source: c.Origin, Observed: time.Now().UTC()})
	}
	call.Validated = true
	if call.Profile.SHA256 == "" {
		call.Question = "Review the retained result and confirm its SHA256."
	}
	// Hashing or a remote COMPLETED claim alone proves nothing. The retained
	// content must satisfy the owner's pre-admitted local done condition.
	for _, e := range call.Evidence {
		if e.SHA256 == call.Profile.SHA256 {
			call.Verified = true
			call.VerifiedHash = e.SHA256
			call.State = "verified"
		}
	}
	if err := s.allowed(ctx, call.Origin, c, call.Profile); err != nil {
		call.Verified = false
		call.State = "unknown"
		return err
	}
	return s.save(ctx, *call)
}
