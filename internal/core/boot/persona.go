// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/persona"
)

// Startup and the serialized conversation lane publish the currently configured
// preset before pinning a turn. Existing preparations keep their saved revision.
func (r *conversationRuntime) refreshPersona(ctx context.Context) error {
	cfg, err := r.controls.ReadConfig()
	if err != nil {
		return err
	}
	core, voice, err := persona.LoadLina(cfg.PersonaSource)
	if err != nil && !r.personaWarned {
		fmt.Fprintln(os.Stderr, persona.SetupWarning)
		r.personaWarned = true
	}
	for {
		err = r.agents.UpdatePreset(ctx, agents.Preset{ID: "lina", Core: core, DefaultVoice: voice}, r.person)
		if !errors.Is(err, agents.ErrConflict) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
