// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// Tasks expose only opaque conversation identities and plan hashes. Snapshot
// paths, source bodies and source credentials stay in the Core-side plan.
func (p *Plan) Tasks() []jobapi.Task {
	tasks := make([]jobapi.Task, len(p.conversations))
	for i, c := range p.conversations {
		body, _ := json.Marshal(struct{ Conversation, Manifest string }{c.record.id, manifest(c)})
		tasks[i] = jobapi.Task{Sequence: int64(i + 1), Input: body}
	}
	return tasks
}

// ApplyJob is the registered deterministic job write rule. It validates both
// Core's durable task and the worker's echo against the prepared immutable plan.
// Core commits the import, job receipt and watermark in this transaction.
func (p *Plan) ApplyJob(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
	if task.Sequence <= 0 || task.Sequence > int64(len(p.conversations)) {
		return jobapi.ErrInvalid
	}
	expected := p.Tasks()[task.Sequence-1]
	if !bytes.Equal(task.Input, expected.Input) || !bytes.Equal(output, expected.Input) {
		return jobapi.ErrConflict
	}
	_, err := p.Apply(ctx, tx, int(task.Sequence-1))
	return err
}
