// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type materialNeeded struct{ ref ledger.Material }

func (*materialNeeded) Error() string { return "material_read_required" }

type materialCache map[ledger.Material]string

func (c materialCache) Read(_ context.Context, ref ledger.Material) (string, error) {
	if body, ok := c[ref]; ok {
		return body, nil
	}
	return "", &materialNeeded{ref: ref}
}
func (materialCache) Prepare(context.Context, secrets.Masked) (ledger.MaterialWrite, error) {
	return nil, canon.ErrReadOnly
}

// Material verification may itself read canon. Never call it while holding the
// sole reader connection. The cache lasts for one operation and is keyed by the
// exact immutable revision. Each retry re-enters a complete current snapshot,
// including retraction and material-trash checks, before using cached bytes.
func (s Source) withMaterials(ctx context.Context, read func(Source) error) error {
	cache := materialCache{}
	snapshot := s
	snapshot.Materials = cache
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := read(snapshot)
		var needed *materialNeeded
		if !errors.As(err, &needed) {
			return err
		}
		if s.Materials == nil {
			return Exclusion("missing_source")
		}
		body, err := s.Materials.Read(ctx, needed.ref)
		if err != nil {
			return err
		}
		cache[needed.ref] = body
	}
}
func (s Source) Load(ctx context.Context, scope Scope, id string) (Episode, error) {
	var episode Episode
	err := s.withMaterials(ctx, func(snapshot Source) error {
		var err error
		episode, err = snapshot.loadSnapshot(ctx, scope, id)
		return err
	})
	if err != nil {
		return Episode{}, err
	}
	return episode, nil
}
func (s Source) ReadEvent(ctx context.Context, scope Scope, id string) (Event, error) {
	var event Event
	err := s.withMaterials(ctx, func(snapshot Source) error {
		var err error
		event, err = snapshot.readEventSnapshot(ctx, scope, id)
		return err
	})
	if err != nil {
		return Event{}, err
	}
	return event, nil
}
