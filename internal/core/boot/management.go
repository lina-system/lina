// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/protocol"
)

func managementError(err error) error {
	if err == nil {
		return nil
	}
	var refusal *clientsrv.OperationError
	if errors.As(err, &refusal) {
		return err
	}
	code := protocol.ErrorUnavailable
	switch {
	case errors.Is(err, agents.ErrNotFound):
		code = protocol.ErrorOutOfScope
	case errors.Is(err, agents.ErrInvalid), errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrScope):
		code = protocol.ErrorMalformed
	case errors.Is(err, memory.ErrSupersedes):
		code = protocol.ErrorStaleRevision
	}
	return &clientsrv.OperationError{Code: code}
}
func malformedManagement() error { return &clientsrv.OperationError{Code: protocol.ErrorMalformed} }
func outsideManagement() error   { return &clientsrv.OperationError{Code: protocol.ErrorOutOfScope} }

func (r *conversationRuntime) Agent(ctx context.Context, s clientsrv.Session, p protocol.AgentRequest) (result protocol.AgentResult, err error) {
	defer func() { err = managementError(err) }()
	if err = r.authorize(s, nil); err != nil {
		return
	}
	result.Operation = p.Operation
	if p.Operation == nil || p.Candidate != nil || p.Revision != nil {
		return result, malformedManagement()
	}
	switch *p.Operation {
	case "list":
		if p.AgentID != nil || p.Name != nil {
			return result, malformedManagement()
		}
	case "create":
		if p.AgentID != nil || p.Name == nil {
			return result, malformedManagement()
		}
		a, e := r.agents.Create(ctx, agents.Create{Name: *p.Name, PersonID: s.PersonID, Preset: "lina"})
		if e != nil {
			return result, e
		}
		result.AgentID = &a.ID
	case "choose", "rename", "delete":
		if p.AgentID == nil {
			return result, malformedManagement()
		}
		if _, err = r.agents.Get(ctx, *p.AgentID); err != nil {
			return
		}
		result.AgentID = p.AgentID
		switch *p.Operation {
		case "choose":
			if p.Name != nil {
				return result, malformedManagement()
			}
			err = r.agents.Select(ctx, *p.AgentID, s.PersonID)
		case "rename":
			if p.Name == nil {
				return result, malformedManagement()
			}
			_, err = r.agents.Rename(ctx, *p.AgentID, *p.Name, s.PersonID)
		case "delete":
			if p.Name != nil {
				return result, malformedManagement()
			}
			// Stop the existing lane before removing this persona from admission.
			r.lanes.Stop("main:"+*p.AgentID, turn.Interrupt{Issuer: turn.Person})
			// Cancel first so a provider waiting for headers releases its
			// dispatch read lock before we fence later requests.
			if r.dispatchMu != nil {
				r.dispatchMu.Lock()
				defer r.dispatchMu.Unlock()
			}
			r.lanes.Stop("main:"+*p.AgentID, turn.Interrupt{Issuer: turn.Person})
			err = r.agents.Delete(ctx, *p.AgentID, s.PersonID, time.Now())
		}
		if err != nil {
			return
		}
	default:
		return result, malformedManagement()
	}
	rows, e := r.agents.List(ctx)
	if e != nil {
		return result, e
	}
	list := make([]protocol.Agent, 0, len(rows))
	for _, a := range rows {
		list = append(list, protocol.Agent{AgentID: a.ID, Name: a.Name, Preset: &a.Preset})
	}
	result.Agents = &list
	return
}

func (r *conversationRuntime) Memory(ctx context.Context, s clientsrv.Session, p protocol.MemoryRequest) (result protocol.MemoryResult, err error) {
	defer func() { err = managementError(err) }()
	if err = r.authorize(s, nil); err != nil {
		return
	}
	if p.Operation == nil || p.AgentID == nil {
		return result, malformedManagement()
	}
	if _, err = r.agents.Get(ctx, *p.AgentID); err != nil {
		return
	}
	result.Operation = p.Operation
	if *p.Operation != "list" && r.dispatchMu != nil {
		r.dispatchMu.Lock()
		defer r.dispatchMu.Unlock()
	}
	scope := memory.RecallScope{Person: s.PersonID, Agent: *p.AgentID}
	store := r.memoryStore()
	query := memory.Query{At: time.Now().Unix(), Limit: 1000}
	if *p.Operation == "list" {
		if p.ItemID != nil || p.Holder != nil || p.Scope != nil {
			return result, malformedManagement()
		}
		if p.After != nil {
			cursor, e := store.Recall(ctx, scope, memory.Query{ID: *p.After, At: query.At, Limit: 1})
			if e != nil {
				return result, e
			}
			if len(cursor) != 1 {
				return result, outsideManagement()
			}
			query.After = *p.After
		}
		if p.Text != nil {
			query.Text = *p.Text
		}
		hits, e := store.Recall(ctx, scope, query)
		if e != nil {
			return result, e
		}
		items := make([]protocol.MemorySummary, 0, len(hits))
		size := 0
		for _, h := range hits {
			evidence := []string{}
			for _, e := range h.Evidence {
				evidence = append(evidence, string(e.Kind)+":"+e.ID)
			}
			item := protocol.MemorySummary{ItemID: h.ID, Content: h.Content, Holder: string(h.Holder), Scope: string(h.Scope), Revision: json.Number(strconv.FormatInt(h.Revision, 10)), Evidence: evidence}
			details := make([]protocol.MemoryEvidence, 0, len(h.Evidence))
			for _, ref := range h.Evidence {
				detail := protocol.MemoryEvidence{Reference: string(ref.Kind) + ":" + ref.ID}
				if ref.Quote != "" {
					detail.Quote = &ref.Quote
				}
				if ref.Status != "" {
					detail.Status = &ref.Status
				}
				if ref.Tombstone != nil {
					detail.Tombstone = &protocol.MemoryTombstone{Agent: ref.Tombstone.Agent, Person: ref.Tombstone.Person, At: json.Number(strconv.FormatInt(ref.Tombstone.At, 10))}
				}
				details = append(details, detail)
			}
			item.EvidenceDetails = &details
			raw, e := json.Marshal(item)
			if e != nil {
				return result, e
			}
			// Optional quotes must not make an otherwise listable item exceed
			// a whole page. Keep its source identities and explain the omission.
			if len(raw)+1 > 512<<10 {
				for i := range details {
					if details[i].Quote != nil {
						details[i].Quote = nil
						status := "quote_too_large"
						details[i].Status = &status
					}
				}
				raw, e = json.Marshal(item)
				if e != nil {
					return result, e
				}
				if len(raw)+1 > 512<<10 {
					item.EvidenceDetails = nil
					raw, e = json.Marshal(item)
					if e != nil {
						return result, e
					}
				}
			}
			// Leave half the transport frame for envelope, metadata and JSON overhead.
			if size+len(raw)+1 > 512<<10 {
				if len(items) == 0 {
					return result, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
				}
				result.Next = &items[len(items)-1].ItemID
				break
			}
			size += len(raw) + 1
			items = append(items, item)
		}
		if len(items) == query.Limit {
			result.Next = &items[len(items)-1].ItemID
		}
		result.Items = &items
		return
	}
	if p.After != nil {
		return result, malformedManagement()
	}
	var previous memory.Item
	if *p.Operation == "correct" || *p.Operation == "forget" {
		if p.ItemID == nil || p.Holder != nil || p.Scope != nil {
			return result, malformedManagement()
		}
		query.ID = *p.ItemID
		// Read by identity through the same scoped recall path; never accept a
		// client's holder/person claim as authority for another agent's item.
		hits, e := store.Recall(ctx, scope, query)
		if e != nil {
			return result, e
		}
		for _, h := range hits {
			if h.ID == *p.ItemID {
				previous = h.Item
				break
			}
		}
		if previous.ID == "" {
			return result, outsideManagement()
		}
	}
	switch *p.Operation {
	case "forget":
		if p.Text != nil {
			return result, malformedManagement()
		}
		err = r.removals.Forget(ctx, retract.Request{Person: s.PersonID, Targets: []retract.Ref{{Kind: retract.Memory, ID: previous.ID}}})
		result.ItemID = p.ItemID
		return
	case "remember", "correct":
		if p.Text == nil || strings.TrimSpace(*p.Text) == "" || len(*p.Text) > 16384 || *p.Operation == "remember" && p.ItemID != nil {
			return result, malformedManagement()
		}
		item := memory.Item{Kind: memory.Fact, Content: *p.Text, Subject: memory.Subject{Kind: memory.Person, ID: s.PersonID}, Basis: memory.Stated, Standing: true}
		if p.Holder != nil {
			item.Holder = memory.Holder(*p.Holder)
			if *p.Holder == "agent" {
				item.Holder = memory.Holder(scope.Agent)
			}
		}
		if p.Scope != nil {
			item.Scope = memory.Scope(*p.Scope)
		}
		if previous.ID != "" {
			item.Holder, item.Scope, item.Subject, item.Kind, item.Supersedes = previous.Holder, previous.Scope, previous.Subject, previous.Kind, previous.ID
			item.Standing = previous.Standing
			if previous.Kind == memory.Name {
				item.Name = &memory.NameValue{Current: *p.Text}
				item.Supersedes = ""
			}
		}
		if item.Holder != "" && item.Holder != memory.All && item.Holder != memory.Holder(scope.Agent) {
			return result, malformedManagement()
		}
		now := time.Now()
		// A management statement is native person evidence in its own ledger lane.
		// It never enters the conversation/model history or a second data store.
		var materials ledger.Materials
		if r.materials != nil {
			bound := *r.materials
			bound.actor.AgentID = scope.Agent
			materials = &bound
		}
		sourceID := rand.Text()
		event, e := ledger.New(r.db, materials).AfterAdmission(r.bindInput).Admit(ctx, guard.EntryOwner, ledger.Input{ID: "memory-input:" + sourceID, ConversationID: "memory-statement:" + sourceID, PersonID: s.PersonID, AgentID: scope.Agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: *p.Text})
		if e != nil {
			return result, e
		}
		item.Evidence = []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}
		receipt, e := memoryexec.NewMemory(r.db, materials, nil).Remember(ctx, memory.Write{Person: s.PersonID, Agent: scope.Agent, Item: item, ExplicitHolder: true})
		if e != nil {
			return result, e
		}
		result.ItemID = &receipt.ID
		return
	default:
		return result, malformedManagement()
	}
}
