// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/worker"

	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

// WorkConfig binds installed integration adapters. These are trusted in-process
// capabilities; no model request or client payload can supply a driver, authority
// policy, canon writer or epoch. Without an installed worker integration, the
// ordinary conversation remains available and delegation is unsupported.
type WorkConfig struct {
	// Installed selects Core-owned production adapters and owner connection settings.
	Installed bool
	Driver    func(*work.Engine) (work.Driver, error)
	MainHost  func(context.Context) (work.Node, error)
	Ready     func(context.Context, *work.Engine, string, string) error
	Changed   func(context.Context, work.State) error
	// Sessions owns worker processes between turns. Core ends a session whose
	// step has landed or been closed; a fixture driver may adopt its own.
	Sessions *worker.Live
}
type workRuntime struct {
	engine *work.Engine
	stop   context.CancelFunc
	done   chan struct{}
	err    error
}

func (w *workRuntime) close(ctx context.Context) error {
	w.stop()
	select {
	case <-w.done:
		return w.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *conversationRuntime) startWork(ctx context.Context, cfg *WorkConfig) error {
	if cfg != nil && cfg.Installed {
		var err error
		cfg, err = r.installedCodeWork()
		if err != nil {
			return err
		}
	}
	if cfg == nil || cfg.Driver == nil {
		return work.ErrInvalid
	}
	sessions := cfg.Sessions
	if sessions == nil {
		sessions = r.liveWorkers
	}
	e, err := work.NewEngine(work.Config{DB: r.db, Records: workDefinitions{r.records}, Epoch: r.epoch, DriverFactory: cfg.Driver, Changed: cfg.Changed, Sessions: sessions,
		AuthorizeGoal: func(ctx context.Context, g work.Goal) error {
			if g.Person != r.person {
				return work.ErrInvalid
			}
			if _, err := r.agents.Get(ctx, g.Agent); err != nil {
				return err
			}
			turnID := g.Turn
			if turnID == "" {
				turnID = "goal-admission:" + g.ID + ":" + g.Agent
			}
			method, err := r.preparedMethod(ctx, turnID, g.Agent)
			if err != nil {
				return err
			}
			if !dispatches(method) {
				return work.ErrInvalid
			}
			return nil
		},
		Refresh: func(ctx context.Context, e *work.Engine) error {
			if cfg.MainHost == nil {
				return nil
			}
			host, err := cfg.MainHost(ctx)
			if err != nil {
				return err
			}
			if host.ID != r.main {
				return errors.New("main host report identity mismatch")
			}
			return e.NodeChanged(ctx, host)
		},
	})
	if err != nil {
		return err
	}
	// Startup derivation precedes publishing the integration's ingress. Its
	// effects wait for the runner, after the durable epoch and canon mirror.
	if err = e.Reconcile(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if cfg.Ready != nil {
		if err = cfg.Ready(ctx, e, r.person, r.agent); err != nil {
			return err
		}
	}
	runCtx, stop := context.WithCancel(ctx)
	w := &workRuntime{engine: e, stop: stop, done: make(chan struct{})}
	if r.dispatchMu != nil {
		r.dispatchMu.Lock()
	}
	r.workEngine = w
	if r.dispatchMu != nil {
		r.dispatchMu.Unlock()
	}
	go func() { w.err = e.Run(runCtx); close(w.done) }()
	return nil
}

type workDefinitions struct{ repo *records.Store }

func (s workDefinitions) Read(ctx context.Context, id string) (work.DefinitionRecord, string, error) {
	r, rev, err := s.repo.Read(ctx, id)
	if errors.Is(err, records.ErrNotFound) {
		err = work.ErrDefinitionMissing
	}
	return work.DefinitionRecord{ID: r.ID, Path: r.Path, Data: r.Data}, rev, err
}
func (s workDefinitions) Commit(ctx context.Context, c work.DefinitionChange, person, agent string) (string, error) {
	return s.repo.Commit(ctx, records.Change{ID: c.ID, Base: c.Base, Path: c.Path, Data: c.Data}, records.Actor{Person: person, Agent: agent})
}
