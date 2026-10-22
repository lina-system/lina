// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// ResultPage reads the pinned material through EOF (including its integrity
// check), then returns a bounded UTF-8 page of the artifact, never checkpoints.
// The authenticated person is supplied by Core, not by model arguments.
func (s *Service) ResultPage(ctx context.Context, person, id string, offset int64) (string, int64, error) {
	r, err := s.read(ctx, id)
	if err != nil {
		return "", 0, err
	}
	if person == "" || person != r.Origin.Person {
		return "", 0, ErrScope
	}
	if r.State != Landed || r.Material.ID == "" {
		return "", 0, ErrState
	}
	reader, err := s.cfg.Assets.OpenRead(ctx, r.Material.ID, r.Material.Revision)
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", 0, err
	}
	var material struct{ Artifact Artifact }
	if err = json.Unmarshal(data, &material); err != nil {
		return "", 0, err
	}
	body := material.Artifact.Body
	if offset < 0 || offset > int64(len(body)) || offset < int64(len(body)) && !utf8.RuneStart(body[offset]) {
		return "", 0, ErrInvalid
	}
	end := min(int64(len(body)), offset+(16<<10))
	for end < int64(len(body)) && !utf8.RuneStart(body[end]) {
		end--
	}
	next := int64(0)
	if end < int64(len(body)) {
		next = end
	}
	return body[offset:end], next, nil
}
