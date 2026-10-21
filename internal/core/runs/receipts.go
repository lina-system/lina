// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

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

// Scope ordinary call receipts to the committed model round. Round zero reads
// legacy records using their original call IDs during recovery.
func receiptKey(r *Record, id string) string {
	if r.ReceiptRound == 0 {
		return id
	}
	return strconv.FormatUint(r.ReceiptRound, 10) + ":" + id
}

// Persist an intent before a scratch handler can change bytes. Scratch replay
// identity includes the full call digest; reusing an ID with different arguments
// is a new action. Unstarted or denied calls never become replay receipts.
func (s *Service) beginTool(ctx context.Context, id string, c provider.ToolCall) (cached *loop.Result, err error) {
	err = s.update(ctx, id, func(r *Record) error {
		key := receiptKey(r, c.ID)
		prior, ok := r.Receipts[key]
		digest := callDigest(c)
		if !ok || prior.Digest != digest {
			return ErrScope
		}
		scratch := s.cfg.Tools[c.Name].Kind == Scratch
		if scratch {
			replay := r.ScratchReceipts[digest]
			// Preserve started scratch intents from records created before round keys.
			if legacy := r.Receipts[c.ID]; !replay.Started && legacy.Started && legacy.Digest == digest {
				replay = legacy
			}
			if replay.Started {
				prior = replay
			}
		}
		if prior.Started {
			result := prior.Result
			if result == nil {
				result = &loop.Result{ID: c.ID, Code: loop.Unknown}
				r.Uncertain = true
			}
			copy := *result
			copy.ID = c.ID
			cached = &copy
			prior.Result = cached
			r.Receipts[key] = prior
			return nil
		}
		prior = ToolReceipt{Digest: digest, Started: true}
		r.Receipts[key] = prior
		if scratch {
			if r.ScratchReceipts == nil {
				r.ScratchReceipts = map[string]ToolReceipt{}
			}
			r.ScratchReceipts[digest] = prior
		}
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
		key := receiptKey(r, c.ID)
		receipt := r.Receipts[key]
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
			r.Receipts[key] = receipt
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
	// Once the round is committed, ordinary receipts duplicate the checkpoint
	// and have no replay authority. Keep only started scratch replay records;
	// preserve legacy unscoped receipts until their recovery is migrated.
	if r.ReceiptRound != 0 {
		for _, call := range r.PendingCalls {
			delete(r.Receipts, receiptKey(r, call.ID))
		}
	}
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
				if r.ReceiptRound == ^uint64(0) {
					return ErrState
				}
				r.ReceiptRound++
				r.PendingCalls = calls
				if r.Receipts == nil {
					r.Receipts = map[string]ToolReceipt{}
				}
				for _, c := range calls {
					r.Receipts[receiptKey(r, c.ID)] = ToolReceipt{Digest: callDigest(c)}
				}
				return nil
			})
		},
		Result: func(ctx context.Context, _ int, _ int, result loop.Result) error {
			return s.update(ctx, id, func(r *Record) error {
				if err := s.transcript(id, "tool_result", result); err != nil {
					return err
				}
				key := receiptKey(r, result.ID)
				receipt, ok := r.Receipts[key]
				if !ok {
					return ErrState
				}
				receipt.Result = &result
				r.Receipts[key] = receipt
				for _, call := range r.PendingCalls {
					if call.ID == result.ID && s.cfg.Tools[call.Name].Kind == Scratch && receipt.Started {
						if r.ScratchReceipts == nil {
							r.ScratchReceipts = map[string]ToolReceipt{}
						}
						r.ScratchReceipts[receipt.Digest] = receipt
					}
				}
				appendRound(r, false)
				return nil
			})
		},
		Partial: func(ctx context.Context, _ int, _ int64, text string) error {
			return s.update(ctx, id, func(r *Record) error { return s.transcript(id, "partial", text) })
		},
	}
}
