// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) startWorkerDispatch(ctx context.Context) error {
	if r.route == nil {
		return nil
	}
	binary, _ := proc.LookPath("agy")
	claude, _ := proc.LookPath("claude")
	if binary == "" && claude == "" {
		return nil
	}
	gh, err := proc.LookPath("gh")
	if err != nil {
		return nil
	}
	git, err := proc.LookPath("git")
	if err != nil {
		return nil
	}
	r.workerGitHub, err = ghclient.New(ctx, ghclient.Config{Path: gh, Leaser: r.activities})
	if err != nil {
		return err
	}
	r.workerDispatch, err = dispatch.Open(ctx, dispatch.Config{DB: r.db, Work: r.work, Area: r.workArea, Skills: r.skills, Root: r.root, Main: r.main, Epoch: r.epoch, Binary: binary, ClaudeBinary: claude, Git: git, PR: r.workerGitHub, Environment: proc.WorkerEnvironment(), Activities: r.activities, Manage: func(ctx context.Context, record dispatch.Record) (dispatch.Record, error) {
		raw, _ := json.Marshal(record.TaskDefinition())
		a, err := r.assignCoding(ctx, record.Origin, record.ID, "dispatch_work", record.Definition.Repository, raw)
		if err != nil {
			return record, err
		}
		if err = r.runCodingManager(ctx, a, "assignment", nil); err != nil {
			return record, err
		}
		record.Owner, record.Manager = a.Owner, a.Conversation
		return record, nil
	}, Publish: func(ctx context.Context, record dispatch.Record) error {
		managerVerdict := "unverified"
		if record.State == "decision" {
			agent, conversation := record.Owner, record.Manager
			if agent == "" {
				agent, conversation = record.Origin.Agent, record.Origin.Conversation
			}
			id := "dispatch:" + record.ID
			rec := recovery.Record{Worker: record.WorkerName(), ID: id, Person: record.Origin.Person, Agent: agent, Conversation: conversation, Input: strings.TrimPrefix(record.Origin.Turn, "turn:"), Workdir: filepath.Join(r.root, "work", "tasks", record.ID), State: "decision", Retries: 1, DecisionAt: record.DecisionAt, Thread: worker.Thread{ID: id, TaskID: record.ID}}
			if err := (recovery.Recovery{DB: r.db}).Admit(ctx, rec); err != nil {
				return err
			}
			r.workers.wakeRecovery()
		}
		if record.Owner != "" && record.State == "finished" {
			var a codingAssignment
			if err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, record.ID); return err }); err != nil {
				return err
			}
			// No receipt means the worker never delivered a judgeable result.
			if card, err := r.work.Get(ctx, record.ID); err == nil && card.Receipt != nil {
				if err = r.runCodingManager(ctx, a, "delivery", &record); err == nil {
					if err = r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, record.ID); return err }); err != nil {
						return err
					}
					managerVerdict = a.Verdict
				}
			}
		}
		view, err := r.router.view(ctx, clientsrv.Session{PersonID: record.Origin.Person}, record.Origin.Agent)
		if err != nil {
			return err
		}
		text := record.WorkerName() + ": " + record.Code
		if record.Version != "" {
			text += " (" + record.Version + "; unverified version)"
		}
		for _, denial := range record.Denied {
			text += "; denied: " + denial.DisplayName
		}
		if record.PR != nil {
			text += fmt.Sprintf("; %s @ %s; adapter-derived receipt; dev manager: %s; not merged", record.PR.URL, record.PR.Head, managerVerdict)
		}
		parent, err := view.journal().Get(ctx, strings.TrimPrefix(record.Origin.Turn, "turn:"))
		if err != nil {
			return err
		}
		now := time.Now()
		noticeID := "worker-result:" + record.ID
		if record.Attempt > 1 || record.State == "unknown" {
			noticeID += fmt.Sprintf(":%d:%s", record.Attempt, record.State)
		}
		event, err := view.journal().Get(ctx, noticeID)
		if errors.Is(err, ledger.ErrNotFound) {
			event, err = view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: noticeID, ConversationID: record.Origin.Conversation, ParentID: strings.TrimPrefix(record.Origin.Turn, "turn:"), AgentID: record.Origin.Agent, Surface: parent.Surface, ScaffoldKind: "worker_result", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
		}
		if err != nil {
			return err
		}
		body, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
		return view.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &body, Speaker: &speaker})
	}})
	return err
}
