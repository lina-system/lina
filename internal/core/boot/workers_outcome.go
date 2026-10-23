// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/tools"
)

// A completion is evidence of what the worker reported, not a delivery verdict
// or a live control handle. Its inbox entry and ledger event commit together.
type workerCompletion struct {
	workerResult
	Workdir    string              `json:"workdir"`
	TaskPacket string              `json:"task_packet"`
	Status     worker.FinishStatus `json:"status"`
	Failure    worker.FailureKind  `json:"failure,omitempty"`
}

// A failed worker's result reaction reports its outcome. Only the recovery
// controller or a later person instruction can authorize another execution.
func failedWorkerReaction(input ledger.Event) bool {
	if input.ScaffoldKind != "worker_completion" {
		return false
	}
	var result workerCompletion
	return json.Unmarshal([]byte(input.Body.Text()), &result) != nil || result.Status != worker.Done
}

func (e *workerSession) finish(ctx context.Context, event worker.Event) error {
	return e.finishOutcome(ctx, event, e.output, true)
}

// Reconciliation queues the normal result reaction without starting a worker turn.
func (e *workerSession) recoveredFinish(ctx context.Context, event worker.Event, output string) error {
	return e.finishOutcome(ctx, event, output, event.Finish.Status == worker.Done)
}
func (e *workerSession) finishOutcome(ctx context.Context, event worker.Event, output string, enqueue bool) error {
	completion := workerCompletion{
		workerResult: workerResult{Owner: e.origin.agent, Manager: e.origin.conversation, Session: event.Thread.ID, Turn: event.Thread.TurnID, Generation: event.Thread.ProcessGeneration, Binding: event.Thread.BindingEpoch, State: worker.Idle, Worker: "codex", Version: e.compatibility.Version, Verified: e.compatibility.Verified, VerifiedVersions: e.compatibility.VerifiedVersions, Output: output},
		Workdir:      e.workdir, TaskPacket: e.packet, Status: event.Finish.Status, Failure: event.Finish.Failure,
	}
	if completion.Failure == worker.ConnectionFailure {
		completion.State = worker.Dead
	}
	raw, err := json.Marshal(completion)
	if err != nil {
		return err
	}
	// Session/turn IDs can be reused by a new process. The Core attempt binds
	// deduplication to this launch, including two launches from the same input.
	digest := sha256.Sum256([]byte(e.input.ID + "\x00" + e.thread.AttemptID + "\x00" + event.Thread.ID + "\x00" + event.Thread.TurnID))
	id := "worker-finish:" + hex.EncodeToString(digest[:])
	// Coding ownership stays with dev; the input and result belong to the
	// requesting conversation, including its original channel restrictions.
	r := e.origin.workerDelivery()
	if _, err := r.journal().Get(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, ledger.ErrNotFound) {
		return err
	}
	if event.Finish.Failure == worker.ConnectionFailure && event.Finish.Code == "connection_lost" {
		// Transport loss is provisional: it must neither occupy the terminal
		// result's identity nor replace a terminal result already recorded above.
		id = "worker-transport-lost:" + hex.EncodeToString(digest[:])
		if _, err := r.journal().Get(ctx, id); err == nil {
			return nil
		} else if !errors.Is(err, ledger.ErrNotFound) {
			return err
		}
	}

	// A failed recovery is delivered by its decision row. It cannot feed an
	// automatic model turn that might create another worker for the same task.
	if !enqueue {
		now := time.Now()
		_, err := r.journal().Admit(ctx, guard.EntryTool, ledger.Input{ID: id, ConversationID: r.conversation, ParentID: e.input.ID, AgentID: r.agent, Surface: e.input.Surface, ScaffoldKind: "worker_completion", Locator: "worker-session:" + event.Thread.ID, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: string(raw)})
		return err
	}
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		return err
	}
	admission := &conversationAdmission{db: r.db, input: inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "worker-finish", NativeID: id}, Body: []byte(masked.Text())}}
	now := time.Now()
	input, err := ledger.New(admission, r.materials).Admit(ctx, guard.EntryTool, ledger.Input{ID: id, ConversationID: r.conversation, ParentID: e.input.ID, AgentID: r.agent, Surface: e.input.Surface, ScaffoldKind: "worker_completion", Locator: "worker-session:" + event.Thread.ID, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: string(raw)})
	if errors.Is(err, errAdmittedInput) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.route == nil {
		return nil
	}
	return r.submitWorkerCompletion(input, admission.receipt.ID)
}

func (r *conversationRuntime) submitWorkerCompletion(input ledger.Event, inboxID int64) error {
	shared := r
	if r.coordinator != nil {
		shared = r.coordinator
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	// Never steer completion into a half-written reply. The existing lane owns
	// ordering, tool receipts, persona identity, failure and durable turn claims.
	return shared.submitInput(input, inboxID, false, r)
}

func (r *conversationRuntime) recordedWorker(ctx context.Context, session string) (workerCompletion, error) {
	var id string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id FROM conv_event WHERE conversation_id=? AND agent_id=? AND scaffold_kind='worker_completion' AND json_extract(origin,'$.locator')=? ORDER BY rowid DESC LIMIT 1`, r.conversation, r.agent, "worker-session:"+session).Scan(&id)
	})
	if err != nil {
		return workerCompletion{}, err
	}
	event, err := r.journal().Get(ctx, id)
	if err != nil {
		return workerCompletion{}, err
	}
	var result workerCompletion
	err = json.Unmarshal([]byte(event.Body.Text()), &result)
	return result, err
}

// Recover admitted, unclaimed worker completions and question-card answers.
// Turn recovery already sealed any
// interrupted model calls; replaying those could repeat an effect.
func (r *conversationRuntime) resumeWorkerCompletions(ctx context.Context) error {
	if r.route == nil {
		return nil
	}
	var pending []struct {
		event string
		inbox int64
	}
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT e.id,i.id FROM inbox i JOIN conv_event e ON e.id=i.native_id LEFT JOIN turn_runs t ON t.inbox_id=i.id WHERE ((i.source='worker-finish' AND e.scaffold_kind='worker_completion') OR (i.source='question-answer' AND e.scaffold_kind='question_answer' AND EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=e.id))) AND t.id IS NULL ORDER BY i.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item struct {
				event string
				inbox int64
			}
			if err := rows.Scan(&item.event, &item.inbox); err != nil {
				return err
			}
			pending = append(pending, item)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, item := range pending {
		event, err := r.journal().Get(ctx, item.event)
		if err != nil {
			return err
		}
		if _, err := r.agents.Get(ctx, event.AgentID); err != nil {
			continue
		}
		view, err := r.workerCompletionView(ctx, event)
		if errors.Is(err, channels.ErrDenied) {
			continue
		}
		if err != nil {
			return err
		}
		if err := view.submitWorkerCompletion(event, item.inbox); err != nil {
			return err
		}
	}
	return nil
}

func (r *conversationRuntime) workerCompletionView(ctx context.Context, event ledger.Event) (*conversationRuntime, error) {
	if event.Surface.Kind != "channel" {
		return r.router.view(ctx, clientsrv.Session{PersonID: r.person}, event.AgentID)
	}
	// A persona's main conversation can be shared by several channels. Resolve
	// the retained originating inbox input, never another binding with the
	// same persona/conversation or just the parent chat of a restricted thread.
	parentID := event.ParentID
	seen := map[string]bool{}
	for !strings.HasPrefix(parentID, "channel-input:") {
		if parentID == "" || seen[parentID] {
			return nil, channels.ErrDenied
		}
		seen[parentID] = true
		parent, err := r.journal().Get(ctx, parentID)
		if errors.Is(err, ledger.ErrNotFound) {
			return nil, channels.ErrDenied
		}
		if err != nil {
			return nil, err
		}
		if parent.AgentID != event.AgentID || parent.ConversationID != event.ConversationID || parent.Surface.Kind != "channel" || parent.Origin.Kind != "native" || parent.ReplayOf != "" {
			return nil, channels.ErrDenied
		}
		// Only Core's durable continuation events can carry the original receipt.
		if parent.ScaffoldKind != "question_answer" && parent.ScaffoldKind != "worker_completion" {
			return nil, channels.ErrDenied
		}
		parentID = parent.ParentID
	}
	parent, ok := strings.CutPrefix(parentID, "channel-input:")
	id, err := strconv.ParseInt(parent, 10, 64)
	if !ok || err != nil || id <= 0 {
		return nil, channels.ErrDenied
	}
	var input channelInput
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT i.id,r.native_id,r.agent,i.conversation,i.body
            FROM channel_receipts r JOIN inbox i ON i.id=r.inbox_id
            WHERE i.id=? AND r.agent=? AND i.conversation=?`, id, event.AgentID, event.ConversationID).Scan(&input.id, &input.native, &input.agent, &input.conversation, &input.body)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, channels.ErrDenied
	}
	if err != nil {
		return nil, err
	}
	original, binding, err := r.channels.channelBinding(ctx, input)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, channels.ErrDenied
	}
	if err != nil {
		return nil, err
	}
	shared, err := r.router.channelView(ctx, binding)
	if err != nil {
		return nil, err
	}
	view := newConversationView(r, binding.Agent, binding.Conversation)
	view.coordinator, view.channelBinding, view.channelAddress = shared, &binding, original.Address
	return view, nil
}

// Only this conversation's worker directory is selectable. The existing file
// worker supplies scope, symlink, file-type, size and sandbox checks; there is no
// unsandboxed read fallback and no model-selected host root or write operation.
func (r *conversationRuntime) readWorkerFile(ctx context.Context, workdir, name string) (tools.Result, error) {
	activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "worker file read"})
	if err != nil {
		return tools.Result{State: tools.Failed}, err
	}
	defer activity.Release()
	versions, err := os.MkdirTemp("", "lina-worker-read-")
	if err != nil {
		return tools.Result{State: tools.Failed}, err
	}
	defer os.RemoveAll(versions)
	owner := proc.NewOwner()
	defer owner.Close(context.WithoutCancel(ctx))
	// The installed executable is intentionally accessible to the file sandbox;
	// the file root is still the worker directory and all private Core data stays
	// denied. In particular a worker cannot select canon or secrets as its root.
	denied := []string{filepath.Join(r.root, "run"), filepath.Join(r.root, "system/core")}
	for _, part := range []string{"canon", "secrets", "versions", "backups", "materials", "derived", "staging"} {
		denied = append(denied, filepath.Join(r.root, part))
	}
	executor, err := coreexec.New(owner, coreexec.Config{Scope: "worker-read", Root: workdir, Versions: versions, Lina: filepath.Join(r.root, "system/bin/lina"), ReadDenied: denied, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		return tools.Result{State: tools.Failed}, err
	}
	result, err := executor.ReadFile(ctx, activity, name)
	if err != nil {
		return result, err
	}
	var reply tools.FileReply
	if err := json.Unmarshal(result.Body, &reply); err != nil {
		return tools.Result{State: tools.Failed}, err
	}
	if !utf8.Valid(reply.Result.Content) {
		return tools.Result{State: tools.Failed, Body: workerError(errors.New("worker file is not UTF-8 text"))}, nil
	}
	body, err := json.Marshal(map[string]string{"path": name, "content": string(reply.Result.Content), "revision": reply.Result.Revision})
	return tools.Result{State: tools.Applied, Body: body, Revision: reply.Result.Revision}, err
}
