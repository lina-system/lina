// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// turnState holds the inputs and mutable state shared by a conversation turn's callbacks.
type turnState struct {
	activity           lease.Lease
	runtime            *conversationRuntime
	input              ledger.Event
	turnID             string
	items              []provider.Item
	events             []ledger.Event
	memoryEpoch        uint64
	memoryRefs         []turn.Reference
	preparedNames      []memory.Item
	bindings           map[string]loop.Tool
	toolCatalog        *turnToolCatalog
	promoted           budget.Handle
	round              int
	filters            map[string]*conversationReplyStream
	completed          []ledger.Admission
	saved              []ledger.Event
	replyShapes        reply.Shapes
	questions          *questionTurn
	questionRoundCalls int
	compaction         *conversationCompaction
}

func (r *conversationRuntime) run(ctx context.Context, inputs *loop.Inputs, input ledger.Event, inboxID int64) (err error) {
	turnID := "turn:" + input.ID
	if err = r.setActive(ctx, true); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, r.setActive(ctx, false)) }()
	activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "conversation"})
	if err != nil {
		return err
	}
	defer activity.Release()
	releaseCredentials := r.vault.BeginTurn()
	defer releaseCredentials()
	s := turnState{
		activity:  activity,
		runtime:   r,
		input:     input,
		turnID:    turnID,
		bindings:  map[string]loop.Tool{},
		filters:   map[string]*conversationReplyStream{},
		questions: &questionTurn{},
	}

	defer func() {
		r.personaMu.Lock()
		delete(r.personaGrants, turnID)
		delete(r.personaGrants, turnID+":state")
		delete(r.personaGrants, turnID+":expression")
		delete(r.personaGrants, turnID+":relationship")
		delete(r.personaGrants, turnID+":names")
		delete(r.personaGrants, turnID+":coding")
		for id, g := range r.personaGrants {
			if g.Identity.TaskID == turnID && g.Identity.Profile == "results" {
				delete(r.personaGrants, id)
			}
		}
		r.personaMu.Unlock()

	}()

	evidence := turn.EvidenceWriter{Ledger: r.journal(), ConversationID: r.conversation, ParentID: input.ID, Surface: input.Surface, Now: time.Now}
	prep := &turn.Preparation{
		VerifyBuild:        r.verifyBuild,
		SelectCapabilities: r.selectProviderCapabilities,
		Account:            func(context.Context) (provider.Credential, error) { return r.route.Request(nil).Credential, nil },
		Resolve: func(context.Context) (turn.ModelPolicy, error) {
			return turn.ModelPolicy{Model: r.route.Model, Revision: "configured", Class: r.route.Adapter.Class()}, nil
		},
		Context:     s.prepare,
		Acknowledge: evidence.Acknowledge, RecordRequest: evidence.RecordRequest,
		Digest: assemble.Digest,
	}
	inputQueue := &conversationInputs{runtime: r, inputs: inputs, questions: s.questions, retainBinding: func(input string, index int, item provider.Item) {
		if s.compaction == nil {
			return
		}
		c := s.compaction
		if c.transient == nil {
			c.transient = map[string]compaction.Record{}
		}
		id := fmt.Sprintf("input-binding:%s:%d", input, index)
		c.transient[id] = compaction.Record{ID: id, Item: append(provider.Item(nil), item...)}
		c.pending[id] = true
	}}
	out, err := turn.New(r.db).Run(ctx, turn.Options{ID: turnID, Conversation: r.conversation, InboxID: inboxID, InputEvent: input.ID, Preparation: prep, Complete: s.complete,
		Finish: func(ctx context.Context, tx *canon.Tx, cause error) error {
			if errors.Is(cause, compaction.ErrContinuation) {
				return s.recordCompactionContinuation(ctx, resultTransaction{tx})
			}
			return nil
		},
		StartFromTurn: func(ctx context.Context, h budget.Handoff) (budget.Handle, error) {
			var err error
			s.promoted, err = r.promoteRun(ctx, input, h)
			return s.promoted, err
		},
		ResultCommitted: r.background.notify, ToolActivity: r.toolActivity, Loop: loop.Config{
			RuntimeTools: r.route.Adapter.Class() == provider.AgentRuntime,
			Tools:        s.bindings,
			Transform: func(ctx context.Context, request provider.Request) (provider.Request, error) {
				request, err := s.toolCatalog.transform(ctx, request)
				if err != nil || s.compaction == nil {
					return request, err
				}
				return s.compaction.transform(ctx, request)
			},
			Before: s.questions.before,
			After: func(ctx context.Context, call provider.ToolCall, result loop.Result) loop.Result {
				return r.resultNotice(ctx, call, r.recordNotice(ctx, input, call, r.skillNotice(ctx, input, call, r.personaNotice(ctx, input, call, result))))
			},
			Inputs: inputQueue,
			Stream: func(ctx context.Context, request provider.Request) (provider.Stream, error) {
				if inputQueue.failure != nil {
					return nil, inputQueue.failure
				}
				s.round++
				s.questionRoundCalls = 0
				if err := r.setPhase(ctx, protocol.TurnPhaseWaitingModel); err != nil {
					return nil, err
				}
				r.dispatchMu.RLock()
				defer r.dispatchMu.RUnlock()
				// Never dispatch a cached preparation after a committed forget. This also
				// fences follow-up rounds; the existing turn flow records the refusal.
				if err := r.validateChannel(ctx); err != nil {
					return nil, err
				}
				if s.round == 1 {
					refs := make([]turn.Reference, 0, len(s.preparedNames))
					for _, name := range s.preparedNames {
						refs = append(refs, turn.Reference{ID: name.ID, Revision: strconv.FormatInt(name.Revision, 10)})
					}
					if err := r.validateModelContext(ctx, s.memoryEpoch, refs); err != nil {
						return nil, err
					}
				}
				if err := r.validateModelContext(ctx, s.memoryEpoch, s.memoryRefs); err != nil {
					return nil, err
				}
				if _, err := r.agents.Get(ctx, r.agent); err != nil {
					return nil, err
				}
				return r.route.Adapter.Stream(ctx, activity, request)
			},
			Observe: s.observe,
		}})
	err = s.publishOutcome(ctx, out, err)
	if err == nil {
		if preparationErr := s.prepareSummaryAfterDelivery(ctx, out); preparationErr != nil {
			_ = s.recordCompaction(ctx, "preparation_failed", struct {
				Code string `json:"code"`
			}{"summary_unavailable"})
		}
	}
	return err
}

func (s *turnState) complete(ctx context.Context, db turn.Canon) error {
	r, input, turnID := s.runtime, s.input, s.turnID
	if s.promoted.ID != "" {
		now := time.Now()
		s.completed = append(s.completed, ledger.Admission{Entry: guard.EntrySystem, Input: ledger.Input{
			ID: "handoff:" + turnID, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface,
			OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", ScaffoldKind: "run_handoff",
			Body: design.Text(eventLanguage(input), "notice.handoff"),
		}})
	}
	var err error
	s.completed, err = conversationTranscript(ctx, db, turnID, input, s.completed)
	if err != nil {
		return err
	}
	if len(s.completed) == 0 {
		return db.Write(ctx, func(*canon.Tx) error { return nil })
	}
	// Adapt the existing filter's generated draft into the same batch as all
	// completed items. Turn completion, sources and cards commit together.
	var batch []ledger.Admission
	for _, item := range s.completed {
		batch = append(batch, item)
		if item.Input.ScaffoldKind == "provider_item" {
			continue
		}
		masked, err := secrets.Mask(item.Input.Body)
		if err != nil {
			return err
		}
		filter, err := conversationFilter(s.replyShapes)
		if err != nil {
			return err
		}
		view, err := filter.Push(masked.Text())
		if err != nil {
			return err
		}
		if item.Entry == guard.EntryAgent {
			batch = append(batch, filter.Check().Admission(item.Input))
		}
		if view.Card != nil {
			card := r.replyCard(item.Input.ID, turnID, view, eventLanguage(input))
			body, err := json.Marshal(card.Reference(item.Input.ID))
			if err != nil {
				return err
			}
			source := item.Input
			batch = append(batch, ledger.Admission{Entry: guard.EntrySystem, Input: ledger.Input{
				ID: card.ResultID, ParentID: source.ID, ConversationID: source.ConversationID, AgentID: source.AgentID,
				Surface: source.Surface, OccurredAt: source.OccurredAt, RecordedAt: source.RecordedAt, TimeQuality: source.TimeQuality,
				ScaffoldKind: "result", Body: string(body),
				Relations: []ledger.Relation{{TargetID: source.ID, Kind: "view_of"}},
			}})
		}
	}
	s.saved, err = ledger.New(namingCanon{Canon: db, person: r.person, agent: r.agent, names: s.preparedNames, replies: batch}, r.materials).AdmitBatch(ctx, batch)
	return err
}

func (s *turnState) observe(ctx context.Context, e provider.Event) error {
	r, input, turnID := s.runtime, s.input, s.turnID
	key := fmt.Sprintf("%s:%d:%d", turnID, s.round, e.Index)
	switch e.Kind {
	case provider.Delta:
		if err := r.setPhase(ctx, protocol.TurnPhaseStreaming); err != nil {
			return err
		}
		filter := s.filters[key]
		if filter == nil {
			base, err := conversationFilter(s.replyShapes)
			if err != nil {
				return err
			}
			filter = &conversationReplyStream{filter: base}
			s.filters[key] = filter
		}
		view, delta, err := filter.Push(e.Delta)
		if err != nil {
			return err
		}
		if delta == "" {
			return nil
		}
		// Persist exactly the safe visible prefix before publishing it.
		// Deferred loop cleanup cannot run after SIGKILL.
		if err := turn.New(r.db).CheckpointPartial(ctx, turnID, turn.Partial{Round: s.round - 1, Index: e.Index, Text: filter.sent}); err != nil {
			return err
		}
		if view.Cut {
			// A result replaces the same stream row, retracting the unfinished
			// sentence while retaining all subsequent text in its preview.
			return r.publishReply(ctx, key, turnID, view, eventLanguage(input))
		}
		return r.publish(ctx, protocol.KindOutputDelta, key, delta)
	case provider.ItemDone:
		var item struct {
			Type, Role string
			Content    []struct{ Type, Text string }
		}
		if err := json.Unmarshal(e.Item, &item); err != nil {
			return err
		}
		if item.Type != "message" || item.Role != "assistant" {
			return nil
		}
		if err := r.setPhase(ctx, protocol.TurnPhaseStreaming); err != nil {
			return err
		}
		var text strings.Builder
		for _, part := range item.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
		now := time.Now()
		s.completed = append(s.completed, ledger.Admission{Entry: guard.EntryAgent, Input: ledger.Input{ID: key, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text.String()}})
		return nil
	case provider.ToolCallReady:
		s.questionRoundCalls++
		return nil
	case provider.Terminal:
		if s.questionRoundCalls == 0 || r.route.Adapter.Class() == provider.AgentRuntime {
			return s.questions.check(ctx)
		}
		return nil
	}
	return nil
}

func (s *turnState) publishOutcome(ctx context.Context, out loop.Outcome, err error) error {
	r, input, turnID := s.runtime, s.input, s.turnID
	if s.promoted.ID != "" {
		// The handle is durable before execution, and the run outlives this turn.
		err = errors.Join(err, r.background.service.Release(context.WithoutCancel(ctx), s.promoted.ID))
	}
	// Provider ItemDone stays provisional until completion and reply admission
	// commit together. Publication can be recovered from history after a crash.
	if err == nil {
		for _, item := range s.saved {
			if item.ScaffoldKind == "provider_item" || item.ScaffoldKind == "result" || item.ScaffoldKind == "reply_check" {
				continue
			}
			if item.SpeakerKind == guard.System {
				text, speaker := item.Body.Text(), protocol.ConversationSpeakerSystem
				err = r.publish(ctx, protocol.KindItemDone, item.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
				if err != nil {
					break
				}
				continue
			}
			filter, filterErr := conversationFilter(s.replyShapes)
			if filterErr != nil {
				err = filterErr
				break
			}
			view, filterErr := filter.Push(item.Body.Text())
			if filterErr != nil {
				err = filterErr
				break
			}
			if publishErr := r.publishReply(ctx, item.ID, turnID, view, eventLanguage(input)); publishErr != nil {
				err = publishErr
				break
			}
		}
	}
	// Late steering remains durable. A follow-up starts through the same lane and
	// turn store with the originating input's runner, retaining its channel scope
	// and binding checks even when the preceding turn began in the app.
	for _, body := range out.FollowUps {
		if pending, ok := r.takeSteering(body); ok {
			_, submitErr := r.queueReaction(r.ctx, r.conversation, func(ctx context.Context, inputs *loop.Inputs) error {
				return pending.runner.run(ctx, inputs, pending.event, pending.inboxID)
			})
			err = errors.Join(err, submitErr)
		}
	}
	if err != nil {
		// A stop or failure is published from its durable outcome. Seal the same
		// streamed block IDs before publishing the stored notice, matching
		// startup history and keeping partials outside future model context.
		stopped, readErr := turn.New(r.db).Stopped(context.WithoutCancel(ctx), input.ID)
		if readErr == nil && stopped.NoticeID != "" {
			if publishErr := r.publishPartials(ctx, stopped); publishErr != nil {
				return errors.Join(err, publishErr)
			}
			notice, noticeErr := r.journal().Get(context.WithoutCancel(ctx), stopped.NoticeID)
			if noticeErr != nil {
				return errors.Join(err, noticeErr)
			}
			if shown, showErr := r.publishCompactionFailure(context.WithoutCancel(ctx), notice, turnID); shown || showErr != nil {
				return errors.Join(err, showErr)
			}
			text := notice.Body.Text()
			speaker := protocol.ConversationSpeakerSystem
			return errors.Join(err, r.publish(ctx, protocol.KindItemDone, notice.ID, protocol.ItemDone{Text: &text, Speaker: &speaker}))
		}
		err = errors.Join(err, readErr)
		text := design.Text(eventLanguage(input), "notice.failed")
		speaker := protocol.ConversationSpeakerSystem
		_ = r.publish(ctx, protocol.KindItemDone, turnID+":status", protocol.ItemDone{Text: &text, Speaker: &speaker})
	}
	return err
}

func (s *turnState) prepare(ctx context.Context, policy turn.ModelPolicy) (turn.PreparedRequest, error) {
	r, input, turnID := s.runtime, s.input, s.turnID
	if err := r.refreshPersona(ctx); err != nil {
		return turn.PreparedRequest{}, err
	}

	persona, err := r.agents.Prepare(ctx, turnID, r.agent)
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	s.items, s.memoryRefs, s.memoryEpoch, s.events, err = r.modelContext(ctx, input.ID)

	if err != nil {
		return turn.PreparedRequest{}, err
	}
	current, err := assemble.History([]ledger.Event{input}, r.conversation, r.agent)
	if err != nil || len(current) != 1 {
		return turn.PreparedRequest{}, assemble.ErrHistory
	}
	// modelContext includes the current event after its filtered history.
	// Keep the assembly's dedicated current-input slot exactly once, and fail
	// closed if retraction removed it between admission and preparation.
	if len(s.items) == 0 || !bytes.Equal(s.items[len(s.items)-1], current[0]) {
		return turn.PreparedRequest{}, assemble.ErrHistory
	}
	s.items = s.items[:len(s.items)-1]
	at := time.Now().UTC()
	minimum := assemble.ContextPacket{{ID: "clock:" + turnID, Kind: assemble.Clock, Text: at.Format(time.RFC3339Nano)}}
	bound, err := r.inputBindingPacket(ctx, input)
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	minimum = append(minimum, bound...)
	settings, err := r.agents.State(ctx, r.agent)
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	s.replyShapes, err = reply.ShapesFrom(agents.ProjectWorkMethod(persona, settings))
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	toolInput := &toolPreparation{ctx: ctx, event: input, persona: persona, settings: settings, at: at, replyShapes: s.replyShapes, questions: s.questions}
	// Declaration order is part of the provider request and its recorded digest.
	// A family binds its tools and returns their declarations in this one pass.
	var declarations []provider.ToolDecl
	for _, prepare := range []toolPreparer{
		r.preparePersonaTools,
		r.prepareSkillTools,
		r.prepareRecordTools,
		r.prepareResultTools,
		r.prepareGoalWorkTools,
		r.prepareMethodologyTools,
		r.preparePlanningTools,
		r.prepareRunTools,
		r.prepareWebTools,
		r.prepareA2ATools,
		r.prepareDispatchTools,
		r.prepareWorkTools,
		r.prepareWorkerTools,
		r.prepareDelegationTools,
		r.prepareTimerTools,
		r.prepareExchangeTools,
	} {
		family, err := prepare(turnID, toolInput, s.bindings)
		if err != nil {
			return turn.PreparedRequest{}, err
		}
		declarations = append(declarations, family...)
	}
	catalog := toolInput.catalog
	minimum = append(minimum, toolInput.packet...)
	methodology, err := r.methodologyContext(ctx)
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	minimum = append(minimum, methodology...)
	namePacket, names, err := r.namePacket(ctx, at)
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	s.preparedNames = names
	minimum = append(minimum, namePacket...)

	if r.coding != nil {
		clear(s.bindings)
		catalog = nil
		declarations, err = r.prepareCodingDecision(turnID, toolInput, s.bindings)
		if err != nil {
			return turn.PreparedRequest{}, err
		}
	}
	if r.channelBinding != nil && !r.channelBinding.Scope.Tools {
		clear(s.bindings)
		declarations = nil
		catalog = nil
	}
	if r.coding == nil && (r.channelBinding == nil || r.channelBinding.Scope.Tools) {
		state, err := r.agents.State(ctx, r.agent)
		if err != nil {
			return turn.PreparedRequest{}, err
		}
		s.toolCatalog = prepareToolGroups(declarations, agents.ProjectWorkMethod(persona, state), s.bindings)
		declarations = s.toolCatalog.first
	}
	if r.channelBinding != nil {
		for name, tool := range s.bindings {
			execute := tool.Execute
			tool.Execute = func(ctx context.Context, call provider.ToolCall) loop.Result {
				if err := r.validateChannel(ctx); err != nil || !r.channelBinding.Scope.Tools {
					return loop.Result{ID: call.ID, Code: loop.Denied}
				}
				return execute(ctx, call)
			}
			s.bindings[name] = tool
		}
	}
	if err := r.bindDoneEvidence(ctx, persona, s.bindings); err != nil {
		return turn.PreparedRequest{}, err
	}
	if failedWorkerReaction(s.input) {
		// Keep the cacheable declarations stable. A failure notice still grants
		// no tool execution, including through the deferred-tool bridge.
		clear(s.bindings)
	}
	packet, _, err := atropos.Packet(ctx, minimum, 0, func(ctx context.Context) (assemble.ContextPacket, error) { return r.relationshipPacket(ctx, at) })
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	if err := pinPersonaState(packet, r.agent, r.conversation); err != nil {
		return turn.PreparedRequest{}, err
	}
	window, count := r.conversationBudget()
	request, report, err := r.compactConversation(ctx, assemble.Input{
		MainID: r.main, ConversationID: r.conversation,
		Core:    assemble.PersonaCoreV1 + "\n" + assemble.TUIFormatting,
		AgentID: r.agent, PersonaRevision: persona.Commit, Persona: persona.Layers,
		AgentName: persona.Name, OwnerPersonID: persona.OwnerPersonID, WorkMethod: persona.WorkMethod,
		Catalog: catalog, AlwaysTools: declarations,
		History: s.items, Current: current[0], Packet: packet,
		Model: policy.Model, Effort: policy.Effort, MaxTokens: window,
	}, s.events, func(ctx context.Context, request provider.Request) (provider.Stream, error) {
		r.dispatchMu.RLock()
		defer r.dispatchMu.RUnlock()
		if err := r.validateModelContext(ctx, s.memoryEpoch, s.memoryRefs); err != nil {
			return nil, err
		}
		if err := r.validateChannel(ctx); err != nil {
			return nil, err
		}
		activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "conversation summary"})
		if err != nil {
			return nil, err
		}
		meter, _ := ctx.Value(summaryUsageKey{}).(*summaryUsage)
		if meter != nil {
			meter.called = true
		}
		stream, err := r.route.Adapter.Stream(ctx, activity, request)
		if err != nil {
			activity.Release()
			return nil, err
		}
		return &summaryStream{Stream: stream, release: activity.Release, meter: meter}, nil
	}, func(c *conversationCompaction) { s.compaction = c })
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	if report.Failure != "" {
		if report.Failure == assemble.TooLarge {
			return turn.PreparedRequest{}, turn.ErrRequestTooLarge

		}
		return turn.PreparedRequest{}, turn.ErrInvalid
	}
	if s.compaction != nil {
		s.compaction.logSizes = func(ctx context.Context, sizes compaction.Sizes) error {
			return s.recordCompaction(ctx, "request_sizes", sizes)
		}
		s.compaction.session, err = compaction.NewSession(s.compaction.pass, s.compaction.view, s.resolveCompactionItems)
		if err != nil {
			return turn.PreparedRequest{}, err
		}
	}
	if s.toolCatalog != nil {
		s.toolCatalog.offset = len(request.Items)
	}
	assembly := turn.Assembly{AgentID: r.agent, PersonaRevision: persona.Commit, WorkMethodRevision: persona.WorkMethodRevision, InstructionFormat: "persona-tui-work-method-v1", RequestLayout: ledger.InputBeforeContextV1, PreparedAt: at.Format(time.RFC3339Nano), Profile: append([]turn.Reference(nil), s.memoryRefs...), AgentName: persona.Name, OwnerPersonID: persona.OwnerPersonID, Items: append([]turn.Reference(nil), s.memoryRefs...)}
	assembly.Context, err = r.recordContextSources(ctx, packet, report)
	if err != nil {
		return turn.PreparedRequest{}, err
	}
	for _, event := range s.events {
		projected, err := assemble.History([]ledger.Event{event}, r.conversation, r.agent)
		if err != nil {
			return turn.PreparedRequest{}, err
		}
		if len(projected) == 0 {
			continue
		}
		digest := sha256.Sum256([]byte(event.Body.Text()))
		assembly.History = append(assembly.History, turn.Reference{ID: event.ID, Revision: hex.EncodeToString(digest[:])})
	}
	for _, ref := range report.Items {
		assembly.Items = append(assembly.Items, turn.Reference{ID: ref.ID, Revision: ref.Revision})
	}
	for _, ref := range report.Skills {
		assembly.Skills = append(assembly.Skills, turn.Reference{ID: ref.ID, Revision: ref.Revision})
	}
	return turn.PreparedRequest{Request: request, Assembly: assembly, Counter: count, MaxTokens: window}, nil
}

func (r *conversationRuntime) verifyBuild(ctx context.Context) error {
	m, err := manifest.Read(filepath.Join(r.root, "system/bin/manifest.json"))
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	file, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	for _, binary := range m.Binaries {
		if binary.Name == "lina" && binary.Size == size && binary.SHA256 == hex.EncodeToString(digest.Sum(nil)) {
			return nil
		}
	}
	return errors.New("installed binary verification failed")
}
