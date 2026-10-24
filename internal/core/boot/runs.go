// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

var runningCapability = protocol.Capability{Name: "running", Version: "1.0"}

// One service and one durable run store serve all conversation views.
type backgroundRuntime struct {
	service   *runs.Service
	list      *runs.RunningList
	sessions  *workerListSource
	notices   *lane.Notices
	wake      chan struct{}
	stop      context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	published map[string]string
}

func (b *backgroundRuntime) notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (r *conversationRuntime) startRuns(ctx context.Context) error {
	if r.route == nil {
		return nil
	}
	b := &backgroundRuntime{wake: make(chan struct{}, 1), published: map[string]string{}}
	r.background = b
	adapters, err := r.runReadTools()
	if err != nil {
		return err
	}
	b.service, err = runs.New(runs.Config{Epoch: r.epoch, DB: r.db, Assets: r.assets, WorkDir: filepath.Join(r.root, "work"),
		SelectCapabilities: r.selectProviderCapabilities, ProviderClass: r.route.Adapter.Class(), DrawMock: r.drawMock,
		Model: r.route.Model, Tools: adapters, Authorize: r.authorizeRun, AuthorizeFollowUp: r.authorizeRunFollowSource, PublishPromotion: turn.PublishPromotion,
		Stream: func(ctx context.Context, req provider.Request) (provider.Stream, error) {
			activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "background run"})
			if err != nil {
				return nil, err
			}
			req.Credential = r.route.Request(nil).Credential
			stream, err := r.route.Adapter.Stream(ctx, activity, req)
			if err != nil {
				activity.Release()
				return nil, err
			}
			return &backgroundStream{Stream: stream, activity: activity}, nil
		},
	})
	if err != nil {
		return err
	}
	b.sessions = &workerListSource{workers: r.workers, person: r.person}
	b.list = runs.NewRunningList(r.db, map[string]runs.ListSource{
		"run": goalQuietRuns{inner: runs.RunListSource{}}, "work": goalQuietWork{inner: runs.WorkListSource{Person: r.person}},
		"session": b.sessions, "a2a": a2aListSource{}, "goal": goalTaskList{},
	})
	b.notices = lane.NewNotices(r.db, nil)
	b.notices.OnDelivered = func(d lane.Delivery) {
		if d.Conversation != "main:"+d.Agent {
			return
		} // Channel worker projects this durable landing to its own chat.
		// Delivery has committed and the lane is at its boundary. Publication is
		// recoverable from the ledger if a client disappears here.
		view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, d.Agent)
		if err == nil {
			_ = view.publishRunNotice(ctx, d.Notices)
		}
	}
	if err := r.lanes.Close(ctx); err != nil {
		return err
	}
	r.lanes = lane.NewWithNotices(b.notices)
	r.identity.TopicCapabilities["running"] = runningCapability
	if err := addOperations(&r.identity, map[protocol.Kind]clientsrv.Operation{
		protocol.KindStop: {Capability: runningCapability, Handle: r.stopRun},
		protocol.KindRunningGet: {Capability: runningCapability, Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
			if err := r.authorize(s, nil); err != nil {
				return clientsrv.Result{}, err
			}
			request := e.Payload.(protocol.RunningGet)
			response := protocol.Running{RunID: request.RunID}
			if request.RunID != nil {
				if request.Action == nil {
					return clientsrv.Result{}, runs.ErrInvalid
				}
				ref := runs.ListRef{Source: "run", ID: *request.RunID}
				if strings.HasPrefix(ref.ID, "a2a:") {
					ref.Source = "a2a"
				}
				if id, ok := strings.CutPrefix(ref.ID, "task:"); ok {
					ref.Source, ref.ID = "work", id
				} else if id, ok := strings.CutPrefix(ref.ID, "goal:"); ok {
					ref.Source, ref.ID = "goal", id
				} else if id, ok := strings.CutPrefix(ref.ID, "session:"); ok {
					ref.Source, ref.ID = "session", id
				}
				switch string(*request.Action) {
				case "open":
					var offset int64
					var err error
					if request.Offset != nil {
						offset, err = request.Offset.Int64()
					}
					if err != nil {
						return clientsrv.Result{}, runs.ErrInvalid
					}
					var text string
					var next int64
					if ref.Source == "work" {
						text, next, err = r.work.SummaryPage(ctx, ref.ID, offset)
					} else if ref.Source == "a2a" {
						text, err = r.a2aRunningSummary(ctx, s.PersonID, ref.ID)
					} else if ref.Source == "session" {
						text, err = r.workerRunningSummary(ctx, s.PersonID, ref.ID, offset)
					} else if ref.Source == "goal" {
						text, next, err = r.goalResultPage(ctx, s.PersonID, ref.ID, offset)
					} else {
						text, next, err = b.service.ResultPage(ctx, s.PersonID, ref.ID, offset)
					}
					if err != nil {
						return clientsrv.Result{}, err
					}
					response.Text = &text
					if next > 0 {
						n := json.Number(strconv.FormatInt(next, 10))
						response.Next = &n
					}
					if err = b.list.Open(ctx, s.PersonID, ref); err != nil {
						return clientsrv.Result{}, err
					}
				case "dismiss":
					if ref.Source == "session" {
						if err := b.sessions.refresh(ctx); err != nil {
							return clientsrv.Result{}, err
						}
					}
					if err := b.list.Dismiss(ctx, s.PersonID, ref); err != nil {
						return clientsrv.Result{}, err
					}
				default:
					return clientsrv.Result{}, runs.ErrInvalid
				}
			} else if request.Action != nil || request.Offset != nil {
				return clientsrv.Result{}, runs.ErrInvalid
			}
			return clientsrv.Result{Kind: protocol.KindRunning, Payload: response}, r.publishRunning(ctx)
		}}}); err != nil {
		return err
	}
	r.chainRefresh(func(ctx context.Context, topic string) error {
		if topic == "running" {
			return r.publishRunning(ctx)
		}
		return nil
	})
	if err = b.service.Recover(ctx); err != nil {
		return err
	}
	// Resume the existing service's one checkpoint retry, including queued starts
	// whose receipt committed just before shutdown. Explicit starts must still
	// pass syncRuns' committed-receipt check. Never re-run a conversation.
	records, err := r.runRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.State == runs.Waiting && record.Retries == 1 && record.Reason == "" && record.StartKey == "" {
			if err = b.service.Release(ctx, record.ID); err != nil {
				return err
			}
		}
	}
	if err = r.syncRuns(ctx); err != nil {
		return err
	}
	if err = r.lanes.ResumeNotices(ctx); err != nil {
		return err
	}
	watch, cancel := context.WithCancel(ctx)
	b.stop, b.done = cancel, make(chan struct{})
	go func() {
		defer close(b.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			changed := b.service.Changed()
			// Failed publication remains durable and is retried at the next wake.
			_ = r.syncRuns(watch)
			select {
			case <-watch.Done():
				return
			case <-changed:
			case <-b.wake:
			case now := <-ticker.C:
				_ = b.service.CheckProgress(watch, now, 5*time.Minute)
			}
		}
	}()
	return nil
}

type backgroundStream struct {
	provider.Stream
	activity lease.Lease
}

func (s *backgroundStream) Close() error { defer s.activity.Release(); return s.Stream.Close() }

// channelRunInbox is the inbox id behind a channel-started run. A selected
// receipt is used when the requesting turn is a different input.
func channelRunInbox(o runs.Origin) (int64, bool, error) {
	eventID := o.ChannelReceipt
	if eventID == "" {
		rest, ok := strings.CutPrefix(o.Turn, "turn:")
		if !ok || !strings.HasPrefix(rest, "channel-input:") {
			return 0, false, nil
		}
		eventID = rest
	}
	raw, ok := strings.CutPrefix(eventID, "channel-input:")
	if !ok || raw == "" {
		return 0, false, runs.ErrScope
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, runs.ErrScope
	}
	return id, true, nil
}

func (r *conversationRuntime) authorizeRun(ctx context.Context, o runs.Origin, b runs.Brief) error {
	if o.Person != r.person || o.Epoch != r.epoch || o.Job != "" || r.route == nil {
		return runs.ErrScope
	}
	if id, ok, err := channelRunInbox(o); ok || err != nil {
		if err != nil {
			return err
		}
		if err = r.db.Read(ctx, func(tx *canon.Tx) error {
			var raw []byte
			if err := tx.QueryRowContext(ctx, "SELECT body FROM inbox WHERE id=?", id).Scan(&raw); err != nil {
				return err
			}
			var e gateway.Inbound
			if json.Unmarshal(raw, &e) != nil {
				return runs.ErrScope
			}
			binding, err := channels.AdmitScope(ctx, tx, e.Address, e.Sender, e.Direct, e.Addressed)
			if err != nil {
				return err
			}
			if binding.Agent != o.Agent || binding.Conversation != o.Conversation || !binding.Scope.Tools || binding.Unreachable {
				return runs.ErrScope
			}
			return nil
		}); err != nil {
			return err
		}
	} else if o.Conversation != "main:"+o.Agent {
		binding, err := r.channelBindingFor(ctx, o.Conversation, o.Agent)
		if err != nil || !binding.Scope.Tools || binding.Unreachable {
			return runs.ErrScope
		}
	}
	_, err := r.agents.Get(ctx, o.Agent)
	if err != nil {
		return err
	}
	// A follow-up may name the partial result it continues. That material must
	// belong to this person. Write-capable adapters stay unwired.
	if err = r.authorizeMaterialRefs(ctx, o.Person, b.Refs); err != nil {
		return err
	}
	for _, name := range b.Allow {
		if name != "skill_index" && name != "read_skill" {
			return runs.ErrScope
		}
	}
	return nil
}

func (r *conversationRuntime) authorizeMaterialRefs(ctx context.Context, person string, refs []runs.Ref) error {
	for _, ref := range refs {
		if ref.ID == "" || ref.Revision < 1 || r.assets == nil {
			return runs.ErrScope
		}
		asset, err := r.assets.Get(ctx, ref.ID)
		if err != nil || asset.Actor.PersonID != person || asset.TrashedAt != 0 || ref.Revision > asset.Revision {
			return runs.ErrScope
		}
	}
	return nil
}

func (r *conversationRuntime) runReadTools() (map[string]runs.Tool, error) {
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	specs, err := coreexec.RegisterSkills(registry, r.skills)
	if err != nil {
		return nil, err
	}
	declarations := map[string]provider.ToolDecl{}
	for _, decl := range coreexec.SkillDeclarations() {
		var name struct{ Name string }
		if err = json.Unmarshal(decl, &name); err != nil {
			return nil, err
		}
		declarations[name.Name] = decl
	}
	adapters := map[string]runs.Tool{}
	for _, spec := range specs {
		if spec.Name != "skill_index" && spec.Name != "read_skill" {
			continue
		}
		adapters[spec.Name] = runs.Tool{Declaration: declarations[spec.Name], Kind: runs.Read,
			Authorize: func(ctx context.Context, record runs.Record, _ provider.ToolCall) error {
				return r.authorizeRun(ctx, record.Origin, record.Brief)
			},
			Handler: loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
				_, err := spec.Schema(json.RawMessage(c.Arguments))
				return "", err
			}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				ctx, cancel := context.WithTimeout(ctx, spec.Deadline)
				defer cancel()
				input, err := spec.Schema(json.RawMessage(c.Arguments))
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Invalid}
				}
				var value any
				if spec.Name == "skill_index" {
					var args struct {
						Methodology string `json:"methodology,omitempty"`
					}
					err = json.Unmarshal(input.Arguments, &args)
					if err == nil {
						value, err = r.skills.Index(ctx, args.Methodology)
					}
				} else {
					var ref skills.Version
					err = json.Unmarshal(input.Arguments, &ref)
					if err == nil {
						value, err = r.skills.Read(ctx, ref)
					}
				}
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Failed}
				}
				body, err := json.Marshal(value)
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Failed}
				}
				return loop.Result{ID: c.ID, Code: loop.OK, Output: body}
			}},
		}
	}
	return adapters, nil
}

func (r *conversationRuntime) runOrigin(turnID string) runs.Origin {
	return runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID, Epoch: r.epoch}
}
func (r *conversationRuntime) promoteRun(ctx context.Context, input ledger.Event, h budget.Handoff) (budget.Handle, error) {
	return r.background.service.StartFromTurn(ctx, r.runOrigin(h.Turn), runs.Brief{
		Goal: input.Body.Text(), Scope: "Complete the whole original request using the gathered evidence and remaining reads and analysis.",
		NotTouch: "Do not replay completed calls or make further effects. Preserve all original constraints and verification requirements, including applied steering.",
		Done:     "The whole original request and its checks are satisfied; otherwise identify precisely what remains and why.",
		Checks:   []runs.Check{{Name: "original request and its verification requirements", Kind: runs.Claimed}}, Result: "summary", Allow: []string{"skill_index", "read_skill"},
	}, h)
}

func (r *conversationRuntime) runRecords(ctx context.Context) ([]runs.Record, error) {
	var records []runs.Record
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM background_runs ORDER BY seq")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var record runs.Record
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &record); err != nil {
				return err
			}
			records = append(records, record)
		}
		return rows.Err()
	})
	return records, err
}

func (r *conversationRuntime) syncRuns(ctx context.Context) error {
	records, err := r.runRecords(ctx)
	if err != nil {
		return err
	}
	conversations := map[string]bool{}
	for _, record := range records {
		if record.Origin.Person != r.person {
			continue
		}
		conversations[record.Origin.Conversation] = true
		// Answer delivery is a per-recipient outbox. An unavailable/deleted
		// agent must not block unrelated runs or Core startup. Keep the answer
		// pending for a later pass (including restoration of that agent).
		if err = r.deliverEffectAnswer(ctx, record); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if !record.Released && record.Promotion == nil && (record.State == runs.Working || record.State == runs.Waiting && record.Reason == "") {
			var committed int
			err = r.db.Read(ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT count(*) FROM turn_calls WHERE turn_id=? AND json_extract(call,'$.Name')='start_run' AND json_extract(result,'$.code')='ok' AND json_extract(result,'$.output.id')=?`, record.Origin.Turn, record.ID).Scan(&committed)
			})
			if err != nil {
				return err
			}
			if committed > 0 {
				if err = r.background.service.Release(ctx, record.ID); err != nil {
					return err
				}
			}
		}
	}
	for conversation := range conversations {
		err = r.background.service.TransferNotices(ctx, conversation, func(ctx context.Context, n runs.Notice) error {
			// A goal task lands as a running-list row. Acknowledging the run
			// without a lane notice is the landing; it is not a second message.
			if !n.EffectQuestion {
				owned, err := r.goalOwnsRun(ctx, n.ID)
				if err != nil {
					return err
				}
				if owned {
					return nil
				}
			}
			language, err := r.turnLanguage(ctx, n.Origin.Turn)
			if err != nil {
				return err
			}
			return r.lanes.Notice(ctx, lane.Notice{ID: n.Key, Run: n.ID, Person: n.Origin.Person, Agent: n.Origin.Agent, Conversation: n.Origin.Conversation,
				Outcome: string(n.Outcome), Summary: runSummary(n, language), Result: n.Material.ID, ResultRevision: n.Material.Revision, ResultType: n.Result, Landed: n.Landed, Surface: ledger.Surface{Kind: "tui"}})
		})
		if err != nil {
			return err
		}
	}
	return r.publishRunning(ctx)
}
func runSummary(n runs.Notice, language design.Language) string {
	if n.EffectQuestion {
		return design.Text(language, "notice.run_effect_question")
	}
	if n.Outcome == runs.InputNeeded {
		return design.Text(language, "notice.run_input_needed")
	}

	var verified, unverified []string
	for _, check := range n.Checks {
		if check.Passed && check.Verified {
			verified = append(verified, check.Check.Name)
		} else {
			unverified = append(unverified, check.Check.Name)
		}
	}
	if len(verified) == 0 {
		verified = []string{design.Text(language, "notice.none")}
	}
	if len(unverified) == 0 {
		unverified = []string{design.Text(language, "notice.none")}
	}
	return fmt.Sprintf(design.Text(language, "notice.run_report"), design.Text(language, "notice.run_"+string(n.Outcome)), strings.Join(verified, ", "), strings.Join(unverified, ", "))

}
func (r *conversationRuntime) publishRunning(ctx context.Context) error {
	b := r.background
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions != nil {
		if err := b.sessions.refresh(ctx); err != nil {
			return err
		}
	}
	snapshot, err := b.list.Build(ctx, r.person)
	if err != nil {
		return err
	}
	current := map[string]bool{}
	for _, entry := range snapshot.Entries {
		if entry.Ref.Source == "work" {
			entry.Ref.ID = "task:" + entry.Ref.ID
		} else if entry.Ref.Source == "goal" {
			entry.Ref.ID = "goal:" + entry.Ref.ID
		} else if entry.Ref.Source == "session" {
			entry.Ref.ID = "session:" + entry.Ref.ID
		}
		current[entry.Ref.ID] = true
		text := entry.View.State + ": " + entry.View.Summary
		if entry.View.Reason != "" {
			text += " (" + entry.View.Reason + ")"
		}
		if b.published[entry.Ref.ID] == text {
			continue
		}
		state := protocol.RunningState(entry.View.State)
		update := protocol.RunningUpdate{Text: &text, RunID: &entry.Ref.ID, State: &state, Summary: &entry.View.Summary}
		if entry.View.Reason != "" {
			reason := protocol.RunningReason(entry.View.Reason)
			update.Reason = &reason
		}
		data, err := json.Marshal(update)
		if err != nil {
			return err
		}
		if err = r.topics.Publish("running", clientsrv.TopicEvent{Kind: protocol.KindRunningUpdate, Key: entry.Ref.ID, Data: data}); err != nil {
			return err
		}
		b.published[entry.Ref.ID] = text
	}
	for id := range b.published {
		if current[id] {
			continue
		}
		removed := true
		data, err := json.Marshal(protocol.RunningUpdate{RunID: &id, Removed: &removed})
		if err != nil {
			return err
		}
		if err = r.topics.Publish("running", clientsrv.TopicEvent{Kind: protocol.KindRunningUpdate, Key: id, Data: data}); err != nil {
			return err
		}
		delete(b.published, id)
	}

	return nil
}
func (r *conversationRuntime) publishRunNotice(ctx context.Context, notices []lane.Notice) error {
	if len(notices) == 0 {
		return nil
	}
	// Match the ledger identity used by lane.Notices (one grouped delivery).
	events, err := r.background.notices.Delivered(ctx, r.conversation)
	if err != nil {
		return err
	}
	for _, delivery := range events {
		if delivery.ID != notices[0].ID {
			continue
		}
		var id string
		err = r.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT id FROM conv_event WHERE conversation_id=? AND json_extract(origin,'$.locator')=?", r.conversation, "background-notice:"+delivery.ID).Scan(&id)
		})
		if err != nil {
			return err
		}
		return r.publishNoticeText(ctx, id, notices)
	}
	return errors.New("missing committed background notice")
}
func (r *conversationRuntime) publishNoticeText(ctx context.Context, key string, notices []lane.Notice) error {
	var summaries []string
	for _, notice := range notices {
		summaries = append(summaries, notice.Summary)
	}
	if r.background != nil {
		if err := r.publishRunQuestions(ctx, notices); err != nil {
			return err
		}
	}
	text := strings.Join(summaries, "\n")
	if err := r.publish(ctx, protocol.KindBackgroundNotice, key, protocol.BackgroundNotice{Text: &text}); err != nil {
		return err
	}
	// Unconfigured Core retains its existing history-only view; no running
	// service or result-read capability has been admitted.
	if r.background == nil {
		return nil
	}
	for _, notice := range notices {
		if notice.Result == "" {
			continue
		}
		record, err := r.background.service.Get(ctx, notice.Run)
		if err != nil {
			return err
		}
		if record.Origin.Person != r.person || record.Origin.Conversation != r.conversation {
			return runs.ErrScope
		}
		// A metadata-only card keeps the result reachable in conversation
		// history after its running row has been opened or dismissed.
		card := protocol.Result{ResultID: notice.Result, Revision: json.Number(strconv.FormatInt(notice.ResultRevision, 10)), Type: protocol.ResultTypeSummary, Status: protocol.ResultStatusPreview,
			Title: "Agent report", Summary: record.Brief.Goal, RunID: &record.ID, RevisedBy: record.Origin.Agent,
			Provenance: protocol.ResultProvenance{AgentID: record.Origin.Agent, ConversationID: record.Origin.Conversation, TurnID: record.Origin.Turn}}
		if err = r.publish(ctx, protocol.KindResult, key+":result:"+record.ID, card); err != nil {
			return err
		}
	}
	return nil
}

// Use the existing Brief wire names so StartTool remains the sole decoder.
func startRunDeclaration() provider.ToolDecl {
	return provider.ToolDecl(`{"type":"function","name":"start_run","description":"Move research, reading, analysis or drawing a UI mock to a background run. For a drawn mock set Result to mock and Goal to the screen request; Core writes a checklist before drawing and returns a checked page and PNG. Feedback sets Mock to the current result_id and revision. Preserve the whole goal, done condition and checks. Returns immediately; never wait for completion. Allowed read tools: skill_index, read_skill.","parameters":{"type":"object","properties":{"Mock":{"type":"object","properties":{"result_id":{"type":"string"},"revision":{"type":"integer","minimum":1}},"required":["result_id","revision"],"additionalProperties":false},"Goal":{"type":"string"},"Scope":{"type":"string"},"NotTouch":{"type":"string"},"Done":{"type":"string"},"Checks":{"type":"array","minItems":1,"items":{"type":"object","properties":{"Name":{"type":"string"},"Kind":{"type":"string","enum":["claimed","tool_called","citations_resolve"]}},"required":["Name","Kind"],"additionalProperties":false}},"Result":{"type":"string"},"Allow":{"type":"array","items":{"type":"string","enum":["skill_index","read_skill"]}}},"required":["Goal","Scope","NotTouch","Done","Checks","Result","Allow"],"additionalProperties":false}}`)
}

func recoverConversationTurns(ctx context.Context, db *canon.Store) error {
	return turn.New(db).RecoverWithReceipts(ctx, runs.RecoverStartReceipt)
}

func (r *conversationRuntime) channelBindingFor(ctx context.Context, conversation, agent string) (channels.Binding, error) {
	rows, err := channels.New(r.db).Connections(ctx, r.person)
	if err != nil {
		return channels.Binding{}, err
	}
	for _, row := range rows {
		if row.Sender == "" {
			continue
		}
		for _, binding := range row.Bindings {
			if binding.Conversation == conversation && binding.Agent == agent {
				return binding, nil
			}
		}
	}
	return channels.Binding{}, channels.ErrDenied
}
