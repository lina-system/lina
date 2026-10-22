// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/persona"
)

// Shared by every conversation lane in this Core, including channel views.
// Serialization prevents an older refresh from publishing after a newer one.
type presetPublisher struct {
	mu       sync.Mutex
	source   fs.FS
	warnings io.Writer
	warned   map[string]bool
}

func newPresetPublisher(source fs.FS, warnings io.Writer) *presetPublisher {
	return &presetPublisher{source: source, warnings: warnings, warned: map[string]bool{}}
}

// At startup load every pair; on a turn refresh only that agent's preset before
// pinning. Already prepared turns retain their saved revision. Legacy private
// configuration and world material never participate in this path.
func (r *conversationRuntime) refreshPersona(ctx context.Context) error {
	if r.presets == nil {
		r.presets = newPresetPublisher(persona.Shipped(), os.Stderr)
	}
	keys := []string{}
	if r.agent == "" {
		for _, s := range persona.Starters() {
			keys = append(keys, s.Key)
		}
	} else {
		a, err := r.agents.Get(ctx, r.agent)
		if err != nil {
			return err
		}
		if !persona.IsStarter(a.Preset) {
			return nil
		}
		keys = append(keys, a.Preset)
	}
	p := r.presets
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, key := range keys {
		core, voice, err := persona.Load(p.source, key)
		if err != nil && !p.warned[key] {
			fmt.Fprintln(p.warnings, persona.SetupWarning(key))
			p.warned[key] = true
		}
		for {
			err = r.agents.UpdatePresetLayers(ctx, key, core, voice, r.person)
			if !errors.Is(err, agents.ErrConflict) {
				break
			}
			if err = ctx.Err(); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
