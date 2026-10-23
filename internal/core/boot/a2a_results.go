// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/result"
)

func (r *conversationRuntime) deliverA2A(ctx context.Context, c a2a.Call) error {
	if !c.Validated || len(c.Evidence) == 0 || c.Origin.Person != r.person {
		return a2a.ErrScope
	}
	view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, c.Origin.Agent)
	if err != nil {
		return err
	}
	if view.conversation != c.Origin.Conversation {
		return a2a.ErrScope
	}
	var e a2a.Evidence
	for _, candidate := range c.Evidence {
		if candidate.SHA256 == c.VerifiedHash || !c.Verified && e.SHA256 == "" {
			e = candidate
			break
		}
	}
	if e.SHA256 == "" {
		return a2a.ErrEvidence
	}
	var prepared *assets.PreparedMaterial
	if len(e.Body) > 128<<10 || !strings.HasPrefix(e.MediaType, "text/") && e.MediaType != "application/json" {
		prepared, err = r.materials.writer.Prepare(ctx, bytes.NewReader(e.Body), "a2a/"+c.ID, assets.Actor{PersonID: c.Origin.Person, AgentID: c.Origin.Agent})
		if err != nil {
			return err
		}
		defer prepared.Close()
	}
	body := result.Summary{Outline: []result.Section{{Heading: "Result", Notes: string(e.Body)}, {Heading: "Source", Notes: fmt.Sprintf("%s\nTask: %s\nArtifact: %s\nObserved: %s\nSHA256: %s", e.Source, c.TaskID, e.Artifact, e.Observed.Format("2006-01-02T15:04:05Z07:00"), e.SHA256)}}, KeyPoints: []string{}, Decisions: []string{}, Questions: []result.Question{}, Scope: result.Coverage{Includes: []string{"Owner-approved content hash verified"}, Excludes: []string{"Proof of unrelated remote effects"}}, Length: 1}
	raw, _ := json.Marshal(body)
	value := result.Result{ID: "a2a:" + c.ID, Revision: 1, Type: result.SummaryType, Title: "Remote agent result", Summary: "The returned content passed the configured check.", Text: result.PlainText(&body, nil), Body: raw, Status: "preview", RevisedBy: c.Origin.Agent, Provenance: result.Provenance{AgentID: c.Origin.Agent, ConversationID: c.Origin.Conversation, TurnID: c.Origin.Turn, TaskID: &c.ID}}
	if !c.Verified {
		value.Title = "Remote agent evidence"
		value.Summary = "Review this output before accepting it."
		body.Scope.Includes = []string{"Retained remote content; verification pending"}
		value.Body, _ = json.Marshal(body)
		value.Text = result.PlainText(&body, nil)
	}
	if c.AcceptedBy != "" {
		value.Revision = 2
		value.Summary = "The owner verified this artifact."
	}
	scope := result.Scope{PersonID: c.Origin.Person, AgentID: c.Origin.Agent, ConversationID: c.Origin.Conversation, TurnID: c.Origin.Turn, TaskID: &c.ID}
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		var count int
		var previous int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM result_heads WHERE id=?", value.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			if err := tx.QueryRowContext(ctx, "SELECT revision FROM result_heads WHERE id=?", value.ID).Scan(&previous); err != nil {
				return err
			}
			if previous >= value.Revision {
				return nil
			}
		}
		if prepared != nil {
			asset, err := prepared.Insert(ctx, tx)
			if err != nil {
				return err
			}
			file := &result.File{Asset: result.AssetRef{ID: asset.ID, Revision: int(asset.Revision)}, Name: "Remote artifact", MediaType: e.MediaType, Size: asset.Size}
			value.Type = result.FileType
			value.Body, _ = json.Marshal(file)
			value.Text = result.PlainText(file, nil)
			value.Summary += " Source: " + e.Source + ". SHA256: " + e.SHA256
		}
		journal := results.LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}
		event, err := journal.Append(ctx, tx, scope, value)
		if err != nil {
			return err
		}
		if count != 0 {
			_, err = tx.ExecContext(ctx, "UPDATE result_heads SET revision=?,event_id=? WHERE id=?", value.Revision, event, value.ID)
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO result_heads(id,person,agent,conversation,revision,event_id) VALUES(?,?,?,?,?,?)", value.ID, scope.PersonID, scope.AgentID, scope.ConversationID, value.Revision, event)
		return err
	})
	if err != nil {
		return err
	}
	if err = view.publishResult(ctx, value); err != nil {
		return err
	}
	return r.remoteAgents.Delivered(ctx, c)
}
func (r *conversationRuntime) recoverA2A(ctx context.Context) error {
	calls, err := r.remoteAgents.Undelivered(ctx)
	if err != nil {
		return err
	}
	for _, c := range calls {
		if c.Validated {
			if err = r.deliverA2A(ctx, c); err != nil {
				var refused *clientsrv.OperationError
				if errors.Is(err, a2a.ErrScope) || errors.As(err, &refused) && refused.Code == protocol.ErrorOutOfScope {
					continue
				}
				return err
			}
		}
	}
	return nil
}
