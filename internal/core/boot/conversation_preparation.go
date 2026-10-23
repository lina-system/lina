// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type conversationPreparation struct {
	mu   sync.Mutex
	jobs *jobs.Service
	id   string
	// A bounded volatile cache, indexed by the opaque task's source snapshot.
	// Losing an entry merely makes the worker acknowledge an obsolete task.
	builders map[string]*compaction.Builder
	order    []string
}

func (r *conversationRuntime) ensureSummaryPreparation(ctx context.Context) error {
	m := r.summaryState()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs != nil {
		return nil
	}
	m.id = summaryPreparationID(r.agent, r.conversation)
	m.builders = map[string]*compaction.Builder{}
	lookup := func(raw json.RawMessage) *compaction.Builder {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.builders[string(raw)]
	}
	service, err := jobs.New(jobs.Config{Store: r.db, Epoch: r.epoch, Limit: 1048576, Definitions: map[string]jobs.Definition{m.id: {
		Agent: r.agent, Account: r.route.Request(nil).Credential.Account, Provider: r.route.Adapter.Provider(),
		Model: func(ctx context.Context, call jobs.Call) (jobs.Result, error) {
			meter := &summaryUsage{}
			ctx = context.WithValue(ctx, summaryUsageKey{}, meter)
			b := lookup(call.Input)
			if b == nil {
				return jobs.Result{Output: json.RawMessage(`{"ID":""}`), Usage: meter.result()}, nil
			}
			raw, err := b.GeneratePreparation(ctx, call.Input)
			return jobs.Result{Output: raw, Usage: meter.result()}, err
		},
		Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, raw json.RawMessage) error {
			b := lookup(task.Input)
			if b == nil {
				return nil
			}
			return b.ApplyPreparation(ctx, tx, task, raw)
		},
	}}})
	if err != nil {
		return err
	}
	if err := service.Ensure(ctx, jobapi.Job{ID: m.id, Kind: jobapi.Memory, Enabled: true, Every: time.Nanosecond, Timeout: time.Minute, Budget: 1048576, MaxTokens: 128000, Adoption: "conversation summary preparation"}); err != nil {
		return err
	}
	m.jobs = service
	return nil
}

func (m *conversationPreparation) candidates() []*compaction.Builder {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*compaction.Builder
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.builders[m.order[i]])
	}
	return out
}

func (s *turnState) prepareSummaryAfterDelivery(ctx context.Context, out loop.Outcome) error {
	c, r := s.compaction, s.runtime
	if c == nil || c.session == nil {
		return nil
	}
	view := c.session.View()
	start := c.initialItems + len(view.Active)
	if start > len(out.Items) {
		return compaction.ErrPass
	}
	last, err := s.resolveCompactionItems(ctx, out.Items[start:])
	if err != nil {
		return err
	}
	view.Active = append(view.Active, last...)
	m := r.summaryState()
	// A completed snapshot no longer has a protected active input. The builder
	// is immutable while the optional worker uses it, including across new turns.
	b := &compaction.Builder{Store: c.builder.Store, Events: c.builder.Events, Agent: c.builder.Agent, Conversation: c.builder.Conversation, Model: c.builder.Model, GenerationRevision: c.builder.GenerationRevision, PreparationJob: summaryPreparationID(r.agent, r.conversation), Now: c.builder.Now, NextID: c.builder.NextID, Generate: c.builder.Generate}
	_, err = b.QueuePreparation(ctx, c.pass, view, func(ctx context.Context, raw json.RawMessage) error {
		if err := r.ensureSummaryPreparation(ctx); err != nil {
			return err
		}
		m.mu.Lock()
		key := string(raw)
		if _, exists := m.builders[key]; !exists {
			m.order = append(m.order, key)
		}
		m.builders[key] = b
		for len(m.order) > 8 {
			delete(m.builders, m.order[0])
			m.order = m.order[1:]
		}
		m.mu.Unlock()
		return m.jobs.ScheduleSnapshot(ctx, m.id, raw)
	})
	return err
}

// Recreate only the Core job binding after restart. Volatile preparation text
// stays lost; an old queued snapshot gets an empty ticket and advances normally.
func (api growthJobAPI) summaryService(ctx context.Context, id string) (*jobs.Service, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "summary:"))
	var identity []string
	if err != nil || json.Unmarshal(raw, &identity) != nil || len(identity) != 2 {
		return nil, jobapi.ErrUnavailable
	}
	var registered int
	err = api.root.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM moirai_jobs WHERE id=? AND EXISTS(SELECT 1 FROM conv_event WHERE agent_id=? AND conversation_id=?)`, id, identity[0], identity[1]).Scan(&registered)
	})
	if err != nil || registered != 1 {
		return nil, errors.Join(jobapi.ErrUnavailable, err)
	}
	view, err := api.root.router.channelView(ctx, channels.Binding{Person: api.root.person, Agent: identity[0], Conversation: identity[1]})
	if err != nil {
		return nil, err
	}
	if err := view.ensureSummaryPreparation(ctx); err != nil {
		return nil, err
	}
	return view.summaryState().jobs, nil
}

// Channel and background-result runners share their conversation's preparation
// cache and job binding, while the captured builder retains its scoped checks.
func (r *conversationRuntime) summaryState() *conversationPreparation {
	if r.coordinator != nil {
		return &r.coordinator.summaryPreparation
	}
	return &r.summaryPreparation
}

func summaryPreparationID(agent, conversation string) string {
	identity, _ := json.Marshal([]string{agent, conversation})
	return "summary:" + base64.RawURLEncoding.EncodeToString(identity)
}

type summaryUsageKey struct{}
type summaryUsage struct {
	called bool
	usage  provider.Usage
}

func (m *summaryUsage) result() provider.Usage {
	if !m.called {
		zero := int64(0)
		return provider.Usage{Input: &zero, Output: &zero}
	}
	return m.usage
}
