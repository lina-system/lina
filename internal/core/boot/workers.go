// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type workerRuntime struct {
	mu       sync.Mutex
	closed   bool
	sessions map[string]*workerSession
	starting sync.WaitGroup
}
type workerSession struct {
	ownerControl    sync.Mutex
	ownerRequests   map[string]string
	reservation     string
	reservationTurn string
	adapter         worker.Adapter
	close           func() error
	compatibility   codex.Compatibility
	origin          *conversationRuntime
	input           ledger.Event
	thread          worker.Thread
	packet          string
	workdir         string
	scheduled       bool
	answered        func(context.Context, worker.Thread)
	started         time.Time
	approvals       map[string]worker.Event
	questions       map[string]*workerQuestion
	output          string
	outputThread    worker.Thread // Scheduled output belongs to one exact execution.
	process         lease.Lease
	mu              sync.Mutex
	active          map[string]lease.Lease
	control         sync.Mutex
	done            chan struct{}
}

type workerArgs struct {
	Kind       string `json:"kind,omitempty"`
	Skill      string `json:"skill,omitempty"`
	Session    string `json:"session,omitempty"`
	Path       string `json:"path,omitempty"`
	Turn       string `json:"turn,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	Binding    uint64 `json:"binding,omitempty"`
	Text       string `json:"text,omitempty"`
	Quote      string `json:"quote,omitempty"`
	Workdir    string `json:"workdir,omitempty"`
	TaskPacket string `json:"task_packet,omitempty"`
}
type workerResult struct {
	Live             bool               `json:"live"`
	Owner            string             `json:"owner"`
	Manager          string             `json:"manager"`
	Session          string             `json:"session"`
	Turn             string             `json:"turn"`
	Generation       uint64             `json:"generation"`
	Binding          uint64             `json:"binding"`
	State            worker.ThreadState `json:"state"`
	Worker           string             `json:"worker"`
	Version          string             `json:"version"`
	Verified         bool               `json:"verified"`
	VerifiedVersions []string           `json:"verified_versions"`
	Output           string             `json:"output"`
}

func workerSchema(name string, raw json.RawMessage) (tools.Input, error) {
	var a workerArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 1<<20 || decoder.Decode(&a) != nil || decoder.Decode(new(any)) != io.EOF {
		return tools.Input{}, tools.ErrInvalid
	}
	allowed := map[string]bool{}
	target := a.Session
	switch name {
	case "worker_start":
		allowed = map[string]bool{"workdir": true, "task_packet": true, "kind": true, "skill": true}
		if !filepath.IsAbs(a.Workdir) || strings.TrimSpace(a.TaskPacket) == "" {
			return tools.Input{}, tools.ErrInvalid
		}
		a.Workdir = filepath.Clean(a.Workdir)
		id, err := skills.MethodID(a.Kind, a.Skill)
		if err != nil {
			return tools.Input{}, tools.ErrInvalid
		}
		a.Skill = id
		target = a.Workdir
	case "worker_input", "worker_interrupt":
		allowed = map[string]bool{"session": true, "turn": true, "generation": true, "binding": true}
		if name == "worker_input" {
			allowed["text"] = true
			if strings.TrimSpace(a.Text) == "" {
				return tools.Input{}, tools.ErrInvalid
			}
		} else {
			allowed["quote"] = true
			if strings.TrimSpace(a.Quote) == "" {
				return tools.Input{}, tools.ErrInvalid
			}
		}
		if a.Generation == 0 || a.Binding == 0 || a.Turn == "" {
			return tools.Input{}, tools.ErrInvalid
		}
	case "worker_read_file":
		allowed["session"], allowed["path"] = true, true
		if !tools.FilePath(a.Path) {
			return tools.Input{}, tools.ErrInvalid
		}
	case "worker_snapshot", "worker_quota":
		allowed["session"] = true
	default:
		return tools.Input{}, tools.ErrInvalid
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for key := range fields {
		if !allowed[key] {
			return tools.Input{}, tools.ErrInvalid
		}
	}
	if target == "" {
		return tools.Input{}, tools.ErrInvalid
	}
	normalized, _ := json.Marshal(a)
	return tools.Input{Arguments: normalized, Target: "worker:" + target, What: name, Where: target, ScopeKey: "worker-session", Write: name != "worker_snapshot" && name != "worker_quota" && name != "worker_read_file"}, nil
}

func workerDeclarations() []provider.ToolDecl {
	control := `"session":{"type":"string"},"turn":{"type":"string"},"generation":{"type":"integer","minimum":1},"binding":{"type":"integer","minimum":1}`
	declaration := func(name, description, properties, required string) provider.ToolDecl {
		raw, _ := json.Marshal(description)
		return json.RawMessage(`{"type":"function","name":"` + name + `","description":` + string(raw) + `,"parameters":{"type":"object","properties":{` + properties + `},"required":[` + required + `],"additionalProperties":false}}`)
	}
	return []provider.ToolDecl{
		declaration("worker_start", "Start the installed Codex on an explicit task in an existing absolute work directory, using its own settings. Returns a session handle and version compatibility. This is a worker session, not verified task delivery. Optional kind selects a method from skill_index methodology=dev, or skill names one catalog ID; Core includes its immutable body in the task packet. Worker requests return to this conversation; automatic recovery and scheduling are separate.", `"workdir":{"type":"string"},"task_packet":{"type":"string"},"kind":{"type":"string"},"skill":{"type":"string"}`, `"workdir","task_packet"`),
		declaration("worker_input", "Continue an idle worker or steer an active turn. Use the current handle from worker_snapshot; stale handles are refused.", control+`,"text":{"type":"string"}`, `"session","turn","generation","binding","text"`),
		declaration("worker_interrupt", "Request cancellation only when the user asks to stop this worker. Quote the current user request exactly. An acknowledgement is not cancellation completion; inspect worker_snapshot.", control+`,"quote":{"type":"string"}`, `"session","turn","generation","binding","quote"`),
		declaration("worker_quota", "Read the installed worker account quota rows, retaining unknown windows and reset times.", `"session":{"type":"string"}`, `"session"`),
		declaration("worker_read_file", "Read a UTF-8 file and its revision in this conversation worker session work directory through the file sandbox. Path is relative. Available from the recorded completion after restart. Use it to check a claimed file effect before reporting the result. An absent revision means the file does not exist.", `"session":{"type":"string"},"path":{"type":"string"}`, `"session","path"`),
		declaration("worker_snapshot", "Read a worker session owned by this conversation or its recorded terminal outcome after restart (live=false, not a control handle). A completed worker turn is not proof of task delivery.", `"session":{"type":"string"}`, `"session"`),
	}
}

func (r *conversationRuntime) prepareWorkerTools(turnID string, preparation *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	input := preparation.event
	if r.workers == nil {
		return workerDeclarations(), nil
	}
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	for _, name := range []string{"worker_start", "worker_input", "worker_interrupt", "worker_snapshot", "worker_quota", "worker_read_file"} {
		spec := tools.Spec{Classify: func(in tools.Input) tools.EffectClass {
			if in.Write {
				return tools.WorkerEffect
			}
			return tools.Research
		}, Name: name, Schema: func(raw json.RawMessage) (tools.Input, error) { return workerSchema(name, raw) }, Conc: 1, Deadline: 30 * time.Second, Placement: tools.Core}
		err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
			return r.workerOperation(ctx, name, turnID, input, in)
		})
		if err != nil {
			return nil, err
		}
		validated := turn.Tool(registry, spec, tools.Call{}, turnID)
		bindings[name] = loop.Tool{Validate: validated.Validate, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
			if _, err := validated.Validate(call); err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			in, err := spec.Schema(json.RawMessage(call.Arguments))
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			if name == "worker_interrupt" {
				var a workerArgs
				_ = json.Unmarshal(in.Arguments, &a)
				if input.SpeakerKind != guard.Person || !strings.Contains(input.Body.Text(), a.Quote) {
					return loop.Result{ID: call.ID, Code: loop.Denied}
				}
			}
			if name == "worker_start" {
				// Existing effects retain the registry's receipt/replay rules. Only
				// a new execution needs manager admission and its temporary hold.
				key := turn.EffectKey(turnID, name, in.Target, call.ID)
				_, lookupErr := r.personaAuthority.Effect(ctx, key)
				if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
					result := loop.Result{ID: call.ID, Code: loop.Failed, Output: workerError(lookupErr)}
					if ctx.Err() != nil {
						result.Code = loop.Aborted
					}
					return result
				}
				if errors.Is(lookupErr, sql.ErrNoRows) {
					// Admission uses the caller's lifecycle, outside the worker's
					// execution deadline and short-lived grant. No worker can run
					// in this phase, so cancellation releases its directory hold.
					assignment, err := func() (assignment codingAssignment, err error) {
						var request workerArgs
						_ = json.Unmarshal(in.Arguments, &request)
						assignment.ID = codingID(turnID, name, string(in.Arguments))
						// Preserve preset refusal evidence before inspecting a path.
						err = authority.CheckPreset(ctx, r.db, r.epoch, authority.PolicyRequest{Key: "preset:coding:" + assignment.ID, Person: r.person, Agent: r.agent, TaskID: turnID, Tool: name, Target: request.Workdir, Class: tools.WorkerEffect, Arguments: in.Arguments})
						if err != nil {
							return assignment, err
						}
						_, assignment, reservation, err := r.workerStartAssignment(ctx, turnID, in)
						if err != nil {
							return assignment, err
						}
						defer func() {
							err = errors.Join(err, r.work.ReleaseWorker(context.WithoutCancel(ctx), reservation))
						}()
						err = r.runCodingManager(ctx, assignment, "assignment", nil)
						if err == nil {
							err = ctx.Err()
						}
						return assignment, err
					}()
					if err != nil {
						result := loop.Result{ID: call.ID, Code: loop.Failed, Output: workerError(err)}
						if errors.Is(err, authority.ErrPresetLimit) {
							result.Code = loop.Denied
							if effect, readErr := r.personaAuthority.Effect(ctx, "preset:coding:"+assignment.ID); readErr == nil {
								result.Output = effect.Result.Body
							}
						} else if ctx.Err() != nil {
							result.Code = loop.Aborted
						}
						return result
					}
				}
			}
			auth := grant.Request{GrantID: turn.EffectKey(turnID, name, in.Target, call.ID) + ":worker", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "worker-session", Revision: strconv.FormatUint(r.epoch, 10)}
			g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Minute), Revisions: map[string]string{in.Target: auth.Revision}, Scope: []grant.Permission{{Tool: name, Target: in.Target, Write: in.Write}}}
			r.personaMu.Lock()
			r.personaGrants[auth.GrantID] = g
			r.personaMu.Unlock()
			defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
			return turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID).Execute(ctx, call)
		}}
	}
	return workerDeclarations(), nil
}

func (r *conversationRuntime) workerStartAssignment(ctx context.Context, turnID string, in tools.Input) (workerArgs, codingAssignment, string, error) {
	var a workerArgs
	_ = json.Unmarshal(in.Arguments, &a)
	method, err := r.skills.ResolveMethod(ctx, a.Kind, a.Skill)
	if err != nil {
		return a, codingAssignment{}, "", err
	}
	a.TaskPacket, err = skills.MethodPacket(a.TaskPacket, method)
	if err != nil {
		return a, codingAssignment{}, "", err
	}
	a.Workdir, err = filepath.EvalSymlinks(a.Workdir)
	if err != nil {
		return a, codingAssignment{}, "", err
	}
	if r.work == nil {
		return a, codingAssignment{}, "", errors.New("work admission unavailable")
	}
	reservation := rand.Text()
	if err = r.work.ReserveWorker(ctx, reservation, a.Workdir); err != nil {
		return a, codingAssignment{}, "", err
	}
	origin := dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID}
	assignment, err := r.assignCoding(ctx, origin, codingID(turnID, "worker_start", string(in.Arguments)), "worker_start", a.Workdir, in.Arguments)
	if err != nil {
		err = errors.Join(err, r.work.ReleaseWorker(context.WithoutCancel(ctx), reservation))
		return a, assignment, "", err
	}
	return a, assignment, reservation, nil
}

func (r *conversationRuntime) workerOperation(ctx context.Context, name, turnID string, input ledger.Event, in tools.Input) (tools.Result, error) {
	return r.workerOperationWithOwner(ctx, name, turnID, input, in, false)
}

// Only the authenticated owner-control path can set owner. It is not a model
// argument and permits steering an active scheduled turn, never starting one.
func (r *conversationRuntime) workerOperationWithOwner(ctx context.Context, name, turnID string, input ledger.Event, in tools.Input, owner bool) (tools.Result, error) {
	if name == "worker_start" || name == "worker_input" || name == "worker_interrupt" {
		defer r.notifyWorkerRunning()
	}
	var a workerArgs
	_ = json.Unmarshal(in.Arguments, &a)
	var reservation string
	release := false
	if name == "worker_start" {
		var assignment codingAssignment
		var err error
		a, assignment, reservation, err = r.workerStartAssignment(ctx, turnID, in)
		if err != nil {
			result := tools.Result{State: tools.Failed, Body: workerError(err)}
			if errors.Is(err, work.ErrControlHeld) {
				return result, nil
			}
			return result, err
		}
		release = true
		admission := r.work
		defer func() {
			if release {
				_ = admission.ReleaseWorker(context.WithoutCancel(ctx), reservation)
			}
		}()
		if !assignment.Delegated {
			return tools.Result{State: tools.Failed, Body: workerError(errors.New("coding_definition_not_delegated"))}, nil
		}
		owner := newConversationView(r.router.root, assignment.Owner, assignment.Conversation)
		owner.codingDelivery = r
		r = owner
		turnID = "coding:" + assignment.ID
	}
	b := r.workers
	if name == "worker_start" {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return tools.Result{State: tools.Failed}, errors.New("worker runtime closed")
		}
		b.starting.Add(1)
		b.mu.Unlock()
		defer b.starting.Done()
		process, err := r.activities.Acquire(r.ctx, lease.Activity{Class: lease.Reported, Reason: "Codex session"})
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		adapter, err := codex.Open(r.ctx, process, codex.Config{})
		if err != nil {
			process.Release()
			return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
		}
		entry := &workerSession{reservation: reservation, adapter: adapter, close: adapter.Close, compatibility: adapter.Compatibility(), origin: r, input: input, process: process, done: make(chan struct{}), active: map[string]lease.Lease{}, packet: a.TaskPacket, workdir: a.Workdir, started: time.Now(), approvals: map[string]worker.Event{}, questions: map[string]*workerQuestion{}}
		activity, err := r.activities.Acquire(r.ctx, lease.Activity{Class: lease.Hard, Reason: "Codex turn"})
		if err != nil {
			adapter.Close()
			process.Release()
			return tools.Result{State: tools.Failed}, err
		}
		if err := r.work.CheckWorker(ctx, reservation); err != nil {
			activity.Release()
			adapter.Close()
			process.Release()
			return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
		}
		release = false // A launch error may have started a turn; retain its reservation.
		thread, err := entry.start(ctx, worker.StartSpec{TaskID: turnID, AttemptID: turnID + ":" + strconv.FormatInt(time.Now().UnixNano(), 10), Workdir: a.Workdir, TaskPacket: a.TaskPacket, Tools: workerWorkTools(), Required: worker.CapabilitySet{worker.StreamProgress, worker.SignalCompletion, worker.WorkTools, worker.RequestApprovals, worker.ReportQuota}})
		if err != nil {
			activity.Release()
			if entry.reservation == "" {
				return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
			}
			return tools.Result{State: tools.Unknown, Body: workerError(err)}, err
		}
		entry.thread = thread
		entry.reservationTurn = thread.TurnID
		entry.active[thread.TurnID] = activity
		// Registry access is brief and never encloses adapter I/O or canon
		// writes: running-list actions also read it from the canon writer.
		b.mu.Lock()
		if b.closed {
			err = errors.New("worker runtime closed")
		} else if b.sessions[thread.ID] != nil {
			err = errors.New("duplicate worker session identity")
		} else {
			b.sessions[thread.ID] = entry
		}
		b.mu.Unlock()
		if err != nil {
			activity.Release()
			adapter.Close()
			process.Release()
			return tools.Result{State: tools.Unknown}, err
		}
		// This notice commits the installed version independently of a model summary.
		notice := fmt.Sprintf("Codex %s session started.", entry.compatibility.Version)
		if !entry.compatibility.Verified {
			notice = fmt.Sprintf("Codex %s is unverified (verified: %s); the session is running.", entry.compatibility.Version, strings.Join(entry.compatibility.VerifiedVersions, ", "))
		}
		err = r.workerNotice(ctx, input, "worker-version:"+thread.ID, notice)
		go entry.observe()
		if err != nil {
			return tools.Result{State: tools.Unknown}, err
		}
		return entry.result(ctx, thread)
	}
	b.mu.Lock()
	entry := b.sessions[a.Session]
	closed := b.closed
	b.mu.Unlock()
	if !closed && entry == nil && (name == "worker_snapshot" || name == "worker_read_file") {
		record, err := r.recordedWorker(ctx, a.Session)
		if err != nil {
			return tools.Result{State: tools.Failed, Body: workerError(errors.New("worker outcome unavailable in this conversation"))}, nil
		}
		if name == "worker_read_file" {
			return r.readWorkerFile(ctx, record.Workdir, a.Path)
		}
		raw, err := json.Marshal(record)
		return tools.Result{State: tools.Applied, Body: raw}, err
	}
	if closed || entry == nil || !entry.origin.workerConversation(r) {
		return tools.Result{State: tools.Failed, Body: workerError(errors.New("worker session unavailable in this conversation"))}, nil
	}
	if name == "worker_read_file" {
		return r.readWorkerFile(ctx, entry.workdir, a.Path)
	}
	if name == "worker_quota" {
		rows, err := entry.adapter.Quota(ctx)
		if err != nil {
			return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
		}
		dispatcher := r.workerDispatch
		if r.router != nil && r.router.root != nil {
			dispatcher = r.router.root.workerDispatch
		}
		if dispatcher != nil {
			if err = dispatcher.ObserveQuota(ctx, entry.adapter.Name(), rows); err != nil {
				return tools.Result{State: tools.Failed}, err
			}
		}
		body, err := json.Marshal(map[string]any{"rows": rows})
		return tools.Result{State: tools.Applied, Body: body}, err
	}
	if entry.scheduled && name == "worker_input" && !owner {
		return tools.Result{State: tools.Failed, Body: workerError(errors.New("scheduled work is controlled by the scheduler"))}, nil
	}
	// Read the authoritative snapshot immediately before any control. The model's
	// handle is compared to it; a thread id alone never authorizes input.
	entry.control.Lock()
	defer entry.control.Unlock()
	thread := entry.thread
	snap, err := entry.adapter.Snapshot(ctx, thread)
	if err != nil {
		return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
	}
	if name == "worker_snapshot" {
		return entry.encode(snap), nil
	}
	if snap.Thread.TurnID != a.Turn || snap.Thread.ProcessGeneration != a.Generation || snap.Thread.BindingEpoch != a.Binding {
		return tools.Result{State: tools.Failed, Body: workerError(errors.New("stale worker handle"))}, nil
	}
	if entry.scheduled && name == "worker_input" && snap.State != worker.Active {
		return tools.Result{State: tools.Failed, Body: workerError(errors.New("scheduled worker is not active"))}, nil
	}
	var activity lease.Lease
	if name == "worker_input" && snap.State == worker.Idle {
		activity, err = r.activities.Acquire(r.ctx, lease.Activity{Class: lease.Hard, Reason: "Codex turn"})
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
	}
	// Keep the new activity private until its returned turn id is bound. A late
	// finish for the prior turn can only release that prior turn's activity.
	entry.mu.Lock()
	if name == "worker_input" && !entry.scheduled && r.work != nil {
		if snap.State == worker.Idle {
			// Snapshot is an authoritative terminal observation too. A delayed
			// finish notification cannot retain the old reservation or release
			// the next turn's reservation once its identity is installed.
			if entry.reservation != "" && entry.reservationTurn == snap.Thread.TurnID {
				if err = r.work.ReleaseWorker(ctx, entry.reservation); err != nil {
					entry.mu.Unlock()
					activity.Release()
					return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
				}
				entry.reservation = ""
				entry.reservationTurn = ""
			}
			reservation := rand.Text()
			if err = r.work.ReserveWorker(ctx, reservation, entry.workdir); err == nil {
				entry.reservation = reservation
				entry.reservationTurn = "unknown:" + reservation
			}
		} else {
			err = r.work.CheckWorker(ctx, entry.reservation)
		}
		if err != nil {
			entry.mu.Unlock()
			activity.Release()
			return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
		}
	}
	switch name {
	case "worker_input":
		if entry.scheduled {
			// Input may start an idle turn. Scheduled owner follow-ups must
			// remain steer-only even if completion races this snapshot.
			steerer, ok := entry.adapter.(interface {
				Steer(context.Context, worker.Thread, worker.Input) error
			})
			if !ok {
				err = errors.Join(worker.ErrInputRefused, errors.New("worker does not support active-turn steering"))
			} else {
				err = steerer.Steer(ctx, snap.Thread, worker.Input{Text: a.Text})
			}
		} else {
			err = entry.adapter.Input(ctx, snap.Thread, worker.Input{Text: a.Text})
		}
	case "worker_interrupt":
		err = entry.adapter.Interrupt(ctx, snap.Thread, worker.UserCancel)
	}
	if name == "worker_input" {
		if errors.Is(err, worker.ErrInputRefused) {
			// No request left the adapter. Preserve an existing turn's binding
			// so its queued completion can drain it. An idle turn's new
			// reservation has no execution and can be released immediately.
			if snap.State == worker.Idle {
				activity.Release()
				if r.work != nil && entry.reservation != "" {
					if releaseErr := r.work.ReleaseWorker(context.WithoutCancel(ctx), entry.reservation); releaseErr != nil {
						entry.mu.Unlock()
						return tools.Result{State: tools.Unknown, Body: workerError(releaseErr)}, releaseErr
					}
					entry.reservation = ""
					entry.reservationTurn = ""
				}
			}
			entry.mu.Unlock()
			return tools.Result{State: tools.Failed, Body: workerError(err)}, nil
		}
		if !entry.scheduled {
			// Input may observe an idle worker after our active snapshot and start
			// another turn. Bind every accepted input before queued completions can
			// acquire mu; ambiguity keeps a reservation that no old event can free.
			key := "unknown:" + rand.Text()
			if err == nil {
				current, readErr := entry.adapter.Snapshot(ctx, snap.Thread)
				if readErr == nil && current.Thread.TurnID != "" && current.State != worker.Unknown && current.State != worker.Dead {
					key = current.Thread.TurnID
				} else if readErr != nil {
					err = readErr
				}
			}
			entry.reservationTurn = key
			if snap.State == worker.Idle {
				entry.active[key] = activity
			} else if key != snap.Thread.TurnID {
				entry.active[key] = entry.active[snap.Thread.TurnID]
				delete(entry.active, snap.Thread.TurnID)
			}
		}
	}
	entry.mu.Unlock()
	if err != nil {
		return tools.Result{State: tools.Unknown, Body: workerError(err)}, err
	}
	return entry.result(ctx, snap.Thread)
}

func workerError(err error) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"error": err.Error()})
	return raw
}
func (e *workerSession) result(ctx context.Context, t worker.Thread) (tools.Result, error) {
	snap, err := e.adapter.Snapshot(ctx, t)
	if err != nil {
		return tools.Result{State: tools.Unknown, Body: workerError(err)}, err
	}
	return e.encode(snap), nil
}
func (e *workerSession) start(ctx context.Context, spec worker.StartSpec) (worker.Thread, error) {
	thread, err := e.adapter.Start(ctx, e.process, spec)
	if err != nil {
		closeErr := e.close()
		e.process.Release()
		// A definitive refusal dispatched no initial turn. Only successful
		// process cleanup permits releasing its durable directory reservation.
		if errors.Is(err, worker.ErrInputRefused) && closeErr == nil {
			releaseErr := e.origin.work.ReleaseWorker(context.WithoutCancel(ctx), e.reservation)
			if releaseErr == nil {
				e.reservation = ""
			}
			err = errors.Join(err, releaseErr)
		}
		err = errors.Join(err, closeErr)
	}
	return thread, err
}

func (e *workerSession) encode(s worker.ThreadSnapshot) tools.Result {
	raw, _ := json.Marshal(workerResult{Live: true, Owner: e.origin.agent, Manager: e.origin.conversation, Session: s.Thread.ID, Turn: s.Thread.TurnID, Generation: s.Thread.ProcessGeneration, Binding: s.Thread.BindingEpoch, State: s.State, Worker: s.Worker, Version: s.Version, Verified: e.compatibility.Verified, VerifiedVersions: e.compatibility.VerifiedVersions, Output: s.Output})
	return tools.Result{State: tools.Applied, Body: raw, Revision: s.Thread.ID + ":" + s.Thread.TurnID}
}
func (e *workerSession) observe() {
	defer func() {
		close(e.done)
		e.origin.notifyWorkerRunning()
		e.origin.notifyOwnerViews()
	}()
	for event := range e.adapter.Events() {
		switch event.Kind {
		case worker.ProgressEvent:
			if event.Progress.Phase == "server_request_resolved" {
				e.stopRequest(event.Progress.ItemID)
			}
			if event.Progress.Phase == "item_completed" && event.Progress.Text != "" {
				e.output = event.Progress.Text
			}
		case worker.UsageEvent:
		case worker.FinishEvent:
			// A transport-loss event is synthesized by the adapter. It proves
			// neither terminal execution nor completed process cleanup. A real
			// turn completion can also report a provider connection failure.
			terminal := event.Finish.Failure != worker.ConnectionFailure || event.Finish.Code != "connection_lost"
			e.stopRequests(event.Thread.TurnID)
			e.mu.Lock()
			if e.origin.work != nil && e.reservationTurn == event.Thread.TurnID && terminal {
				if err := e.origin.work.ReleaseWorker(context.WithoutCancel(e.origin.ctx), e.reservation); err == nil {
					e.reservation = ""
					e.reservationTurn = ""
				}
			}
			if terminal {
				e.active[event.Thread.TurnID].Release()
				delete(e.active, event.Thread.TurnID)
			}
			e.mu.Unlock()
			if err := e.finish(e.origin.ctx, event); err != nil && e.origin.ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "worker completion admission failed: %v\n", err)
			}
			e.output = ""
		case worker.ApprovalEvent:
			e.requestApproval(event)
		case worker.QuestionEvent:
			e.requestQuestion(event)
		case worker.ToolCallEvent:
			e.workTool(event)
		}
		if event.Kind != worker.UsageEvent && (event.Kind != worker.ProgressEvent || event.Progress.Phase == "started" || event.Progress.Phase == "server_request_resolved") {
			e.origin.notifyWorkerRunning()
		}
		e.origin.notifyOwnerViews()
	}
	e.stopRequests("")
	e.close()
	e.process.Release()
	e.mu.Lock()
	for _, activity := range e.active {
		activity.Release()
	}
	e.active = map[string]lease.Lease{}
	e.mu.Unlock()
}
func (r *conversationRuntime) workerNotice(ctx context.Context, parent ledger.Event, id, text string) error {
	r = r.workerDelivery()
	now := time.Now()
	event, err := r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: r.conversation, ParentID: parent.ID, AgentID: r.agent, Surface: parent.Surface, ScaffoldKind: "worker_session", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	if err != nil {
		return err
	}
	speaker := protocol.ConversationSpeakerSystem
	body := event.Body.Text()
	return r.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &body, Speaker: &speaker})
}
func (r *conversationRuntime) workerDelivery() *conversationRuntime {
	if r.codingDelivery != nil {
		return r.codingDelivery
	}
	return r
}
func (r *conversationRuntime) workerConversation(other *conversationRuntime) bool {
	delivery := r.workerDelivery()
	return r.person == other.person && (r.agent == other.agent && r.conversation == other.conversation || delivery.agent == other.agent && delivery.conversation == other.conversation)
}
func (b *workerRuntime) close() {
	b.mu.Lock()
	b.closed = true
	entries := make([]*workerSession, 0, len(b.sessions))
	for _, e := range b.sessions {
		entries = append(entries, e)
	}
	b.mu.Unlock()
	// Starts registered before shutdown finish or clean up their unregistered
	// processes before Close returns. Waiting never holds the registry mutex.
	b.starting.Wait()
	for _, e := range entries {
		e.close()
		<-e.done
		e.process.Release()
	}
}
