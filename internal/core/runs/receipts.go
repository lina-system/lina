// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type ToolReceipt struct {
	Digest  string
	Started bool
	Result  *loop.Result
}

func callDigest(c provider.ToolCall) string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Persist an intent before a scratch handler can change bytes. A completed call
// reuses its stored result; an interrupted intent never executes a second time.
func (s *Service) beginTool(ctx context.Context, id string, c provider.ToolCall) (cached *loop.Result, err error) {
	err = s.update(ctx, id, func(r *Record) error {
		if r.Receipts == nil {
			r.Receipts = map[string]ToolReceipt{}
		}
		prior, ok := r.Receipts[c.ID]
		digest := callDigest(c)
		if ok && prior.Digest != digest {
			return ErrScope
		}
		if prior.Result != nil {
			result := *prior.Result
			cached = &result
			return nil
		}
		if prior.Started {
			result := loop.Result{ID: c.ID, Code: loop.Unknown}
			cached = &result
			r.Uncertain = true
			return nil
		}
		r.Receipts[c.ID] = ToolReceipt{Digest: digest, Started: true}
		return nil
	})
	return
}
func appendRound(r *Record, recovering bool) {
	if len(r.PendingCalls) == 0 {
		return
	}
	var results []provider.Item
	for _, c := range r.PendingCalls {
		receipt := r.Receipts[c.ID]
		result := receipt.Result
		if result == nil {
			if !recovering {
				return
			}
			code := loop.Aborted
			if receipt.Started {
				code = loop.Unknown
				r.Uncertain = true
			}
			result = &loop.Result{ID: c.ID, Code: code}
			receipt.Result = result
			r.Receipts[c.ID] = receipt
		}
		kind := "function_call_output"
		if c.Custom {
			kind = "custom_tool_call_output"
		}
		payload, _ := json.Marshal(result)
		b, _ := json.Marshal(map[string]string{"type": kind, "call_id": c.ID, "output": string(payload)})
		results = append(results, b)
	}
	r.Checkpoint = append(r.Checkpoint, r.PendingItems...)
	r.Checkpoint = append(r.Checkpoint, results...)
	r.PendingItems = nil
	r.PendingCalls = nil
}
func (s *Service) checkpointJournal(id string) loop.Journal {
	return loop.Journal{
		Item: func(ctx context.Context, _ int, _ int64, b provider.Item) error {
			return s.update(ctx, id, func(r *Record) error {
				if err := s.transcript(id, "item", b); err != nil {
					return err
				}
				r.PendingItems = append(r.PendingItems, b)
				if a, ok := artifact([]provider.Item{b}); ok {
					r.Artifact = a
				}
				return nil
			})
		},
		Calls: func(ctx context.Context, _ int, calls []provider.ToolCall) error {
			return s.update(ctx, id, func(r *Record) error {
				r.PendingCalls = calls
				if r.Receipts == nil {
					r.Receipts = map[string]ToolReceipt{}
				}
				for _, c := range calls {
					if _, ok := r.Receipts[c.ID]; !ok {
						r.Receipts[c.ID] = ToolReceipt{Digest: callDigest(c)}
					}
				}
				return nil
			})
		},
		Result: func(ctx context.Context, _ int, _ int, result loop.Result) error {
			return s.update(ctx, id, func(r *Record) error {
				if err := s.transcript(id, "tool_result", result); err != nil {
					return err
				}
				receipt, ok := r.Receipts[result.ID]
				if !ok {
					return ErrState
				}
				receipt.Result = &result
				r.Receipts[result.ID] = receipt
				appendRound(r, false)
				return nil
			})
		},
		Partial: func(ctx context.Context, _ int, _ int64, text string) error {
			return s.update(ctx, id, func(r *Record) error { return s.transcript(id, "partial", text) })
		},
	}
}
