// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

const StartToolName = "start_run"

type Receipt struct {
	ID    string `json:"id"`
	State State  `json:"state"`
}

// StartTool binds the trusted spawning origin outside the model's arguments.
// The conversation tool journal publishes its result before calling Release on
// the receipt id. Neither this tool nor a run exposes a wait-for-completion tool.
func (s *Service) StartTool(origin Origin) loop.Tool {
	decode := func(c provider.ToolCall) (Brief, error) {
		var b Brief
		if c.Name != StartToolName || c.ID == "" || c.Custom {
			return b, ErrInvalid
		}
		d := json.NewDecoder(bytes.NewBufferString(c.Arguments))
		d.DisallowUnknownFields()
		if err := d.Decode(&b); err != nil {
			return b, ErrInvalid
		}
		if err := d.Decode(new(any)); err != io.EOF || !valid(origin, b) {
			return b, ErrInvalid
		}
		return b, nil
	}
	return loop.Tool{Validate: func(c provider.ToolCall) (string, error) { _, err := decode(c); return "", err }, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		b, err := decode(c)
		if err != nil {
			return loop.Result{ID: c.ID, Code: loop.Invalid}
		}
		id, state, err := s.startWithCall(ctx, origin, b, nil, c.ID)
		if err != nil {
			code := loop.Denied
			if err == ErrInvalid {
				code = loop.Invalid
			} else if err != ErrBudget {
				code = loop.Failed
			}
			return loop.Result{ID: c.ID, Code: code}
		}
		body, _ := json.Marshal(Receipt{ID: id, State: state})
		return loop.Result{ID: c.ID, Code: loop.OK, Output: body}
	}}
}
