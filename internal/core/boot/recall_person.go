// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

// A replay sink accounts only in its output artifact. Live calls reserve and
// settle against the same canon budget used by other auxiliary requests.
func (r *conversationRuntime) recallPersonTool(run string, now func() int64, replayUsage func(provider.Meta)) (tools.Spec, tools.Handler) {
	scope := memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true}
	spec, _ := coreexec.NewMemory(r.db, r.materials, nil).RecallPersonTool(scope, now)
	return spec, func(ctx context.Context, l lease.Lease, in tools.Input) (tools.Result, error) {
		snapshot, err := r.removals.Snapshot(ctx, r.person, r.agent)
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		var aux coreexec.Auxiliary
		if r.route != nil {
			aux = coreexec.NewRecallAuxiliary(r.route.Request(nil), func(ctx context.Context, request provider.Request) (provider.Stream, error) {
				r.dispatchMu.RLock()
				defer r.dispatchMu.RUnlock()
				if err := r.removals.Validate(ctx, snapshot.Epoch); err != nil {
					return nil, err
				}
				if err := r.validateChannel(ctx); err != nil {
					return nil, err
				}
				if _, err := r.agents.Get(ctx, r.agent); err != nil {
					return nil, err
				}
				return r.recallPersonStream(ctx, l, request, run, replayUsage)
			})
		}
		_, handler := coreexec.NewMemory(r.db, r.materials, aux).RecallPersonTool(scope, now)
		result, err := handler(ctx, l, in)
		// Sources and derived sentences are both invalid after a concurrent forget.
		if visibilityErr := r.removals.Validate(ctx, snapshot.Epoch); visibilityErr != nil {
			return tools.Result{State: tools.Failed}, visibilityErr
		}
		if scopeErr := r.validateChannel(ctx); scopeErr != nil {
			return tools.Result{State: tools.Failed}, scopeErr
		}
		return result, err
	}
}

func (r *conversationRuntime) recallPersonStream(ctx context.Context, l lease.Lease, request provider.Request, run string, replayUsage func(provider.Meta)) (provider.Stream, error) {
	credential, err := r.route.Account()
	if err != nil {
		return nil, err
	}
	request.Credential = credential
	reservation := ""
	if replayUsage == nil {
		_, count := r.conversationBudget()
		reservation = "recall:" + rand.Text()
		err := r.db.Write(ctx, func(tx *canon.Tx) error {
			return budget.Reserve(ctx, tx, budget.Reservation{ID: reservation, Agent: r.agent, Account: credential.Account, Provider: r.route.Adapter.Provider(), Kind: provider.Auxiliary, Run: run, Tokens: int64(count(request)) + 4096}, 1048576)
		})
		if err != nil {
			return nil, err
		}
	}
	stream, err := r.route.Adapter.Stream(ctx, l, request)
	if err != nil {
		return nil, err
	}
	return &recallPersonUsageStream{Stream: stream, finish: func(meta provider.Meta) error {
		if replayUsage != nil {
			replayUsage(meta)
			return nil
		}
		// Cancellation must not erase a known charge. Bound receipt completion
		// separately; an unavailable writer leaves the reservation charged.
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		return r.db.Write(settleCtx, func(tx *canon.Tx) error { return budget.Settle(settleCtx, tx, reservation, meta.Usage) })
	}}, nil
}

type recallPersonUsageStream struct {
	provider.Stream
	finish func(provider.Meta) error
}

func (s *recallPersonUsageStream) Close() error {
	err := s.Stream.Close()
	return errors.Join(err, s.finish(s.Stream.Meta()))
}
