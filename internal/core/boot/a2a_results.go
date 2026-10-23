// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/result"
)

func (p *publicationRuntime) observeWork(ctx context.Context, rec a2a.Record, d dispatch.Record, fresh bool) error {
	set := func(state string) error {
		_, err := p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
			if !a2a.Terminal(current.Task.Status.State) {
				a2a.SetState(current, state, time.Now())
			}
			return nil
		})
		return err
	}
	if d.State == "unknown" || d.State == "decision" {
		return set(a2a.Unspecified)
	}
	if d.State != "finished" {
		return nil
	}
	if d.Canceled {
		return set(a2a.Unspecified)
	}
	if d.Code != "ready_for_review" || d.PR == nil || len(d.Denied) > 0 {
		return set(a2a.Failed)
	}
	if !fresh {
		return nil
	}
	if d.FailureReason != "" {
		return set(a2a.Failed)
	}
	if err := p.requireTools(ctx, rec); err != nil {
		return set(a2a.Failed)
	}
	repo, _, ok := strings.Cut(strings.TrimPrefix(d.PR.URL, "https://github.com/"), "/pull/")
	if !ok {
		return set(a2a.Failed)
	}
	rr, err := review.New(p.root.db).Get(ctx, review.Key{Repository: repo, Number: d.PR.Number, Head: d.PR.Head})
	if err != nil {
		return err
	}
	if rr.State == "checking" {
		return nil
	}
	if rr.State != "complete" || rr.Verdict != "verified" {
		return set(a2a.Failed)
	}
	card, err := p.root.work.Get(ctx, d.ID)
	if err != nil {
		return err
	}
	if card.Active || card.Receipt == nil || card.Run == nil || card.Head != d.PR.Head || card.CompletionHead != d.PR.Head || card.Receipt.EventID != rr.Receipt || card.Run.Generation != rr.Generation || !slices.Contains(d.ChangedFiles, rec.Definition.Artifact.Path) {
		return set(a2a.Failed)
	}
	if err = d.PR.Verify(repo, d.TaskDefinition().Branch, d.Definition.Base, d.PR.Head); err != nil {
		return set(a2a.Failed)
	}
	body, err := p.readArtifact(ctx, rec, d)
	if err != nil {
		return set(a2a.Failed)
	}
	// A secret-looking artifact is retained by the worker, never disclosed. The
	// typed result's own masking remains a second independent boundary.
	masked, err := secrets.Mask(string(body))
	if err != nil || masked.Text() != string(body) || !utf8.Valid(body) {
		return set(a2a.Failed)
	}
	sum := sha256.Sum256(body)
	metadata, _ := json.Marshal(map[string]string{"sha256": hex.EncodeToString(sum[:]), "commit": d.PR.Head})
	artifact := a2a.Artifact{ArtifactID: "result", Name: filepath.Base(rec.Definition.Artifact.Path), Parts: []a2a.Part{{Raw: body, Filename: filepath.Base(rec.Definition.Artifact.Path), MediaType: rec.Definition.Artifact.MediaType}}, Metadata: metadata}
	if err = p.authorize(ctx, rec); err != nil {
		return err
	}
	_, err = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
		if a2a.Terminal(current.Task.Status.State) {
			return nil
		}
		if current.CancelRequested {
			return a2a.ErrDenied
		}
		current.Task.Artifacts = []a2a.Artifact{artifact}
		a2a.SetState(current, a2a.Completed, time.Now())
		return nil
	})
	return err
}

type artifactOutput struct {
	bytes.Buffer
	limit int
}

func (b *artifactOutput) Write(raw []byte) (int, error) {
	if b.Len()+len(raw) > b.limit {
		return 0, errors.New("artifact exceeds output limit")
	}
	return b.Buffer.Write(raw)
}
func (p *publicationRuntime) readArtifact(ctx context.Context, rec a2a.Record, d dispatch.Record) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	git, err := proc.LookPath("git")
	if err != nil {
		return nil, err
	}
	l, err := p.root.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "verify published task artifact"})
	if err != nil {
		return nil, err
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	output := &artifactOutput{limit: rec.Definition.Artifact.MaxBytes}
	// Read immutable committed bytes, never a path or symlink from the worker's
	// mutable workspace. Head and relative path both come from trusted records.
	err = owner.Run(ctx, l, proc.Command{Path: git, Args: []string{"-C", filepath.Join(p.root.root, "work", "tasks", d.ID), "show", d.PR.Head + ":" + rec.Definition.Artifact.Path}, Env: proc.WorkerEnvironment(), Stdout: output, Stderr: io.Discard})
	if err != nil {
		return nil, err
	}
	if !strings.Contains(output.String(), rec.Definition.Artifact.Contains) {
		return nil, a2a.ErrInvalid
	}
	return output.Bytes(), nil
}
func (p *publicationRuntime) deliver(ctx context.Context, rec a2a.Record) error {
	if err := p.notice(ctx, rec); err != nil {
		return err
	}
	if rec.Task.Status.State != a2a.Completed || len(rec.Task.Artifacts) != 1 {
		_, err := p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error { current.Published = true; return nil })
		return err
	}
	view, err := p.root.router.view(ctx, clientsrv.Session{PersonID: p.root.person}, rec.Agent)
	if err != nil {
		return err
	}
	// Result head, ledger revision and delivery marker share the canon commit.
	// Repeated scans and restart cannot create a second owner result.
	err = p.root.db.Write(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM a2a_tasks WHERE id=?", rec.Task.ID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return err
		}
		if rec.Delivered != "" {
			return nil
		}
		scope := result.Scope{PersonID: p.root.person, AgentID: rec.Agent, ConversationID: rec.Conversation, TurnID: "turn:a2a-input:" + rec.Task.ID, TaskID: &rec.LocalWork}
		body, _ := json.Marshal(result.Draft{Blocks: []result.Block{{Kind: "paragraph", Text: string(rec.Task.Artifacts[0].Parts[0].Raw)}}})
		store, err := results.New(resultTransaction{tx}, results.Config{Journal: measuredResultJournal{LedgerJournal: results.LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, config: conversationReplyConfig(nil)}})
		if err != nil {
			return err
		}
		card, err := store.Present(ctx, scope, result.PresentRequest{Type: result.DraftType, Title: rec.Definition.Name, Summary: "External task completed and verified.", Body: body})
		if err != nil {
			return err
		}
		rec.Delivered = card.ID
		raw, err = json.Marshal(rec)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE a2a_tasks SET record=? WHERE id=?", raw, rec.Task.ID)
		return err
	})
	if err != nil {
		return err
	}
	if a2aPublished(view, "result-card:"+rec.Delivered) {
		_, err := p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error { current.Published = true; return nil })
		return err
	}
	var eventID string
	if err = p.root.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT event_id FROM result_heads WHERE id=?", rec.Delivered).Scan(&eventID)
	}); err != nil {
		return err
	}
	source, err := view.journal().Get(ctx, eventID)
	if err != nil {
		return err
	}
	if err = view.publishTypedResult(ctx, source); err != nil {
		return err
	}
	_, err = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error { current.Published = true; return nil })
	return err
}
