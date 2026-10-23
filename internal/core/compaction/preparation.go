// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// preparationInput contains locators and fingerprints, never another transcript.
// Core owns the job binding and model route; the worker only relays Aux/Apply.
type preparationInput struct {
	Segment       PassSegment
	Expected      Frontier
	Sources       [32]byte
	Configuration [32]byte
}

// Only this opaque ticket crosses Aux/Apply. Derived text is volatile, so
// generic durable job receipts cannot retain a deleted conversation summary.
type preparationTicket struct{ ID string }
type preparationResult struct {
	ID      string
	Input   preparationInput
	Text    Text
	BuiltAt time.Time
}

func (b *Builder) configuration() [32]byte {
	raw, _ := json.Marshal([]any{b.Agent, b.Conversation, b.Model, b.Manager, PromptVersion, b.GenerationRevision})
	return sha256.Sum256(raw)
}

// QueuePreparation is called after answer delivery, with the completed turn's
// view. It enqueues only at the configured threshold, using the foreground
// segment selection. It starts no goroutine, timer, model call or rollup.
// Enqueue supplies the registered job id and consecutive durable task sequence.
func (b *Builder) QueuePreparation(ctx context.Context, p Pass, completed View, enqueue func(context.Context, json.RawMessage) error) (bool, error) {
	if b.PreparationJob == "" || enqueue == nil {
		return false, ErrPass
	}
	p, err := p.setup(completed)
	if err != nil {
		return false, err
	}
	sizes, err := p.measure(completed)
	if err != nil || sizes.Total < sizes.Threshold {
		return false, err
	}
	completed = copyView(completed)
	completed.History = append(completed.History, completed.Input)
	completed.History = append(completed.History, completed.Active...)
	completed.Input = Record{}
	completed.Active = nil
	segment, err := p.boundarySegment(completed)
	if err != nil {
		return false, err
	}
	if len(segment.Records) == 0 {
		return false, nil
	}
	raw, err := b.PreparationTask(ctx, segment)
	if err != nil {
		return false, err
	}
	return true, enqueue(ctx, raw)
}

// PreparationTask captures immutable source and configuration identity. It does
// not cache or publish a summary. The returned input is enqueued through Core.
func (b *Builder) PreparationTask(ctx context.Context, segment PassSegment) (json.RawMessage, error) {
	p, resolved, err := b.capture(ctx, segment)
	if err != nil {
		return nil, err
	}
	locators := func(records []Record) []Record {
		out := make([]Record, len(records))
		for i, r := range records {
			out[i].ID = r.ID
		}
		return out
	}
	segment = PassSegment{RootID: segment.RootID, Records: locators(segment.Records), Verbatim: locators(segment.Verbatim), Protected: locators(segment.Protected)}
	return json.Marshal(preparationInput{segment, p.Expected, segmentKey(resolved), b.configuration()})
}

// GeneratePreparation is the Core Aux callback, using the same Generate route
// as on-demand compaction. It never makes a result visible before Apply commits.
func (b *Builder) GeneratePreparation(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var input preparationInput
	if json.Unmarshal(raw, &input) != nil {
		return nil, ErrSummary
	}
	discard := func() (json.RawMessage, error) { return json.Marshal(preparationTicket{}) }
	if input.Configuration != b.configuration() {
		return discard()
	}
	p, resolved, err := b.capture(ctx, input.Segment)
	if obsoletePreparation(err) {
		return discard()
	}
	if err != nil {
		return nil, err
	}
	if p.Expected != input.Expected || segmentKey(resolved) != input.Sources {
		return discard()
	}
	text, err := b.Generate(ctx, resolved)
	if err != nil {
		return nil, err
	}
	if !text.valid() {
		return nil, ErrSummary
	}
	result := &preparationResult{ID: rand.Text(), Input: input, Text: text, BuiltAt: b.Now()}
	b.mu.Lock()
	b.workerReady = result
	b.mu.Unlock()
	return json.Marshal(preparationTicket{ID: result.ID})
}

// ApplyPreparation is a jobs.Definition.Apply callback. The job service commits
// only the ticket with its receipt and watermark, never summary text or a node.
func (b *Builder) ApplyPreparation(ctx context.Context, tx *canon.Tx, task jobapi.Task, raw json.RawMessage) error {
	var input preparationInput
	var ticket preparationTicket
	if b.PreparationJob == "" || json.Unmarshal(task.Input, &input) != nil || json.Unmarshal(raw, &ticket) != nil {
		return ErrSummary
	}
	// A worker cannot substitute its own output or promote another task.
	var generated []byte
	if err := tx.QueryRowContext(ctx, "SELECT output FROM moirai_aux WHERE job=? AND sequence=?", b.PreparationJob, task.Sequence).Scan(&generated); err != nil {
		return err
	}
	if !bytes.Equal(generated, raw) {
		return ErrSummary
	}
	b.mu.Lock()
	result := b.workerReady
	b.mu.Unlock()
	// Restart/eviction loses speculative text. Acknowledge an unusable ticket so
	// the worker can advance to the next task; foreground compaction still works.
	if ticket.ID == "" || result == nil || result.ID != ticket.ID {
		return nil
	}
	expected, _ := json.Marshal(input)
	actual, _ := json.Marshal(result.Input)
	if !bytes.Equal(expected, actual) {
		return ErrSummary
	}
	if input.Configuration != b.configuration() {
		return nil
	}
	err := checkFrontier(ctx, tx, input.Expected)
	if obsoletePreparation(err) {
		return nil
	}
	return err
}

func (b *Builder) workerPreparation(ctx context.Context) (*preparedPass, error) {
	if b.PreparationJob == "" {
		return nil, nil
	}
	b.mu.Lock()
	result := b.workerReady
	b.mu.Unlock()
	if result == nil || result.Input.Configuration != b.configuration() {
		return nil, nil
	}
	var raw []byte
	err := b.Store.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT proposal FROM moirai_applied WHERE job=? ORDER BY sequence DESC LIMIT 1", b.PreparationJob).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt jobapi.Proposal
	var ticket preparationTicket
	if json.Unmarshal(raw, &receipt) != nil || json.Unmarshal(receipt.Output, &ticket) != nil || receipt.JobID != b.PreparationJob || ticket.ID != result.ID {
		return nil, nil
	}
	encoded, _ := json.Marshal(result.Text)
	output := Output{Model: string(b.Model), PromptVersion: PromptVersion, BuiltAt: result.BuiltAt, Text: string(encoded), Dropped: result.Text.Omitted}
	if !validOutput(output) {
		return nil, nil
	}
	return &preparedPass{sourceKey: result.Input.Sources, configuration: result.Input.Configuration, proposal: Proposal{Expected: result.Input.Expected, Output: output}, text: result.Text}, nil
}

// boundarySegment is shared by after-delivery preparation and foreground fitting.
func (p Pass) boundarySegment(v View) (PassSegment, error) {
	segment := PassSegment{RootID: v.RootID, Root: v.Root}
	if v.Input.ID != "" {
		segment.Protected = append(segment.Protected, v.Input)
	}
	segment.Protected = append(segment.Protected, v.Active...)
	// Keep a contiguous suffix of complete turns. Retaining only user items
	// would put summarized calls/replies after a retained event, violating the
	// summary tree's prefix coverage and splitting protocol round trips.
	start, budget := len(v.History), 0
	for i := len(v.History) - 1; i >= 0; i-- {
		n := p.itemTokens(v.Request, v.History[i].Item)
		if n < 0 {
			return PassSegment{}, ErrPass
		}
		budget += n
		if budget > p.Policy.KeepUserTokens {
			break
		}
		if v.History[i].User {
			start = i
		}
	}
	segment.Records = copyRecords(v.History[:start])
	segment.Verbatim = copyRecords(v.History[start:])
	return segment, nil
}

func obsoletePreparation(err error) bool {
	return errors.Is(err, ErrStaleFrontier) || errors.Is(err, ErrStaleEpoch) || errors.Is(err, ErrCoverage) || errors.Is(err, ErrUnavailable)
}
