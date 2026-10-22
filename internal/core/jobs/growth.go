// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// GrowthDefinition adopts a precomputed Lachesis candidate. Core registers it
// as a deterministic memory job bound to an agent/person. The worker cannot
// choose the agent, change another layer, or replace the queued candidate.
func GrowthDefinition(store *agents.Store, agent, person string) (Definition, error) {
	if store == nil || strings.TrimSpace(agent) == "" || strings.TrimSpace(person) == "" {
		return Definition{}, jobapi.ErrInvalid
	}
	decode := func(raw json.RawMessage) (agents.GrowthCandidate, error) {
		var c agents.GrowthCandidate
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return c, jobapi.ErrInvalid
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return c, jobapi.ErrInvalid
		}
		return c, nil
	}
	// A client frame is at most 1 MiB. JSON re-encoding can expand one
	// decoded byte into six (for example '<' into a Unicode escape).
	return Definition{Agent: agent, MaxPayloadBytes: maxPayloadBytes, Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		queued, err := decode(task.Input)
		if err != nil {
			return err
		}
		proposed, err := decode(output)
		if err != nil {
			return err
		}
		if queued != proposed {
			return jobapi.ErrInvalid
		}
		_, err = store.AdoptGrowthTx(ctx, tx, agent, person, proposed)
		switch {
		case errors.Is(err, agents.ErrGrowthUndone):
			// Suppression is durable. Consume without another notice or revision.
			return nil
		case errors.Is(err, agents.ErrInvalid):
			return jobapi.ErrInvalid
		case errors.Is(err, agents.ErrConflict):
			return jobapi.ErrConflict
		default:
			return err
		}
	}}, nil
}
