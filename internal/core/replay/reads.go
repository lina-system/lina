// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/tools"
)

func LocalRead(name string) bool { return name == "expand" || name == "recall_person" }

// ReadGate is the discarding authority dependency for the replay read registry.
// Classification comes from Core's registered schema, never a model declaration.
type ReadGate struct{}

func (ReadGate) Begin(_ context.Context, c tools.Call, s tools.Spec, in tools.Input) (tools.Ticket, error) {
	if !LocalRead(s.Name) || in.Write || in.Network || s.Outward {
		return tools.Ticket{}, tools.ErrInvalid
	}
	return tools.Ticket{Key: c.Key}, nil
}
func (ReadGate) Finish(context.Context, tools.Ticket, tools.Result) error { return nil }

func (s Source) ReadEvent(ctx context.Context, scope Scope, id string) (Event, error) {
	var event ledger.Event
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		if err := visible(ctx, tx, scope, id); err != nil {
			return err
		}
		var err error
		event, err = ledger.New(readTx{tx}, s.Materials).Get(ctx, id)
		return err
	})
	if err != nil {
		return Event{}, err
	}
	return eventValue(event), nil
}
