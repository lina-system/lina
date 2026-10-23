// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func (r *conversationRuntime) replaySource() replay.Source {
	return replay.Source{Instructions: replayInstructions, Rebuild: rebuildPersonaItems, Skill: r.replaySkill, Digest: assemble.Digest, DB: r.db, Materials: r.materials, Persona: r.agents.Prepared, Project: func(events []ledger.Event, conversation, agent string) ([]provider.Item, error) {
		return assemble.History(events, conversation, agent)
	}}
}
func replayFailure(err error) error {
	if errors.Is(err, replay.ErrInvalid) {
		return &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	return &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
}
func (r *conversationRuntime) Export(ctx context.Context, s clientsrv.Session, p protocol.ExportRequest) (protocol.ExportResult, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.ExportResult{}, err
	}
	var q replay.Selection
	if p.AgentID != nil {
		q.Agent = *p.AgentID
	}
	if p.Conversations != nil {
		q.Conversations = *p.Conversations
	}
	var err error
	if p.Since != nil {
		q.Since, err = time.Parse(time.RFC3339Nano, *p.Since)
		if err != nil {
			return protocol.ExportResult{}, replayFailure(replay.ErrInvalid)
		}
	}
	if p.Until != nil {
		q.Until, err = time.Parse(time.RFC3339Nano, *p.Until)
		if err != nil {
			return protocol.ExportResult{}, replayFailure(replay.ErrInvalid)
		}
	}
	if q.Agent != "" && q.Agent != r.agent {
		view, err := r.router.view(ctx, s, q.Agent)
		if err != nil {
			return protocol.ExportResult{}, err
		}
		return view.Export(ctx, s, p)
	}
	for _, id := range q.Conversations {
		if err := r.authorize(s, &id); err != nil {
			return protocol.ExportResult{}, err
		}
	}
	path, manifest, err := r.replaySource().StageExport(ctx, r.root, replay.Scope{Person: s.PersonID, Agent: r.agent}, q)
	if err != nil {
		return protocol.ExportResult{}, replayFailure(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return protocol.ExportResult{}, replayFailure(err)
	}
	return protocol.ExportResult{Directory: path, Manifest: string(raw)}, nil
}

func (r *conversationRuntime) Replay(ctx context.Context, s clientsrv.Session, p protocol.ReplayRequest) (protocol.ReplayResult, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.ReplayResult{}, err
	}
	if p.Fixture != nil {
		return r.providerFixture(ctx, s, p)
	}
	if p.Provider != nil && !r.providerReplayAllowed(p) {
		return protocol.ReplayResult{}, replayFailure(replay.ErrInvalid)
	}
	if (p.TurnID == nil) == (p.Episode == nil) {
		return protocol.ReplayResult{}, replayFailure(replay.ErrInvalid)
	}
	var original *replay.Episode
	id := ""
	if p.TurnID != nil {
		id = *p.TurnID
	} else {
		dec := json.NewDecoder(strings.NewReader(*p.Episode))
		dec.DisallowUnknownFields()
		original = new(replay.Episode)
		if dec.Decode(original) != nil || dec.Decode(new(any)) != io.EOF || original.Version != 1 {
			return protocol.ReplayResult{}, replayFailure(replay.ErrInvalid)
		}
		id = original.TurnID
	}
	// Select the agent from the owner's canonical input, never from uploaded
	// episode metadata. Source.Load still gates all content and provenance.
	var agent string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT e.agent_id FROM turn_control c JOIN conv_event e ON e.id=c.input_event WHERE c.turn_id=? AND e.person_id=?`, id, s.PersonID).Scan(&agent)
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	if err == nil && agent != r.agent {
		view, err := r.router.view(ctx, s, agent)
		if err != nil {
			return protocol.ReplayResult{}, err
		}
		return view.Replay(ctx, s, p)
	}
	source := r.replaySource()
	ep, err := source.Load(ctx, replay.Scope{Person: s.PersonID, Agent: r.agent}, id)
	excluded := func(err error) (protocol.ReplayResult, error) {
		var reason replay.Exclusion
		if errors.As(err, &reason) || errors.Is(err, replay.ErrExcluded) {
			text := err.Error()
			return protocol.ReplayResult{ExcludedReason: &text}, nil
		}
		return protocol.ReplayResult{}, replayFailure(err)
	}
	if err != nil {
		return excluded(err)
	}
	if err := r.authorize(s, &ep.Conversation); err != nil {
		return protocol.ReplayResult{}, err
	}
	if original != nil {
		// An episode is a reference to immutable local evidence, not authority to
		// revive old exported bytes. Rehydrate against current removal/scope rules.
		if original.Conversation != ep.Conversation || original.Input.ID != ep.Input.ID || original.Input.Revision != ep.Input.Revision || original.Evidence.Hash != ep.Evidence.Hash {
			return excluded(replay.Exclusion("source_changed"))
		}
	}
	if r.route == nil {
		return protocol.ReplayResult{}, replayFailure(errors.New("no model"))
	}
	candidates := map[string]string{}
	if p.Candidates != nil {
		for _, file := range *p.Candidates {
			if _, exists := candidates[file.Name]; exists {
				return protocol.ReplayResult{}, replayFailure(replay.ErrInvalid)
			}
			candidates[file.Name] = file.Text
		}
	}
	model, effort := "", ""
	if p.Model != nil {
		model = *p.Model
	}
	if p.Effort != nil {
		effort = *p.Effort
	}
	request, err := replay.Request(ep, candidates, model, effort, replayInstructions)
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	executable, err := os.Executable()
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "replay"})
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	defer activity.Release()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	reads, err := r.replayReads(source, ep)
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	usage := []provider.Meta{}
	report := replay.ProviderReport{Version: 1, Provider: r.route.Adapter.Provider(), Route: r.route.Route, Model: request.Model, Source: replaySourceDigest(ep.Evidence.Hash), Requests: []replay.RequestReport{}}
	round := 0
	out, err := replay.Sandboxed(ctx, replay.SandboxConfig{Reads: reads, Executable: executable, Denied: r.replayDenied(), Environment: []string{"PATH=" + os.Getenv("PATH")}, Stream: func(ctx context.Context, request provider.Request) (provider.Stream, error) {
		if err := source.ValidateEpisode(ctx, ep); err != nil {
			return nil, err
		}
		if p.Provider != nil {
			round++
			if round > 16 {
				return nil, replay.ErrInvalid
			}
			request.Layout = ep.Evidence.Layout.Clone()
			if request.Layout != nil && len(request.Items) > len(ep.Items) {
				request.Layout.Newest = &provider.Position{Items: len(request.Items), Tools: len(request.Tools)}
			}
			var err error
			request, err = r.selectProviderCapabilities(request)
			if err != nil {
				return nil, err
			}
			request.Credential = r.route.Request(nil).Credential
			return report.Observe(ep.TurnID, round, r.route.Route, time.Now, func() (provider.Stream, error) { return r.route.Adapter.Stream(ctx, activity, request) }, &request.Bridge), nil
		}
		request.Credential = r.route.Request(nil).Credential
		stream, err := r.route.Adapter.Stream(ctx, activity, request)
		if err != nil {
			return nil, err
		}
		return &replayUsageStream{Stream: stream, save: func(meta provider.Meta) { usage = append(usage, meta) }}, nil
	}}, replay.WorkerInput{RuntimeTools: r.route.Adapter.Class() == provider.AgentRuntime, Request: request, Calls: ep.Calls})
	if p.Provider != nil {
		if err == nil {
			err = source.ValidateEpisode(ctx, ep)
		}
		return providerReportOutput(report, err)
	}
	if err != nil {
		return excluded(err)
	}
	if err = source.ValidateEpisode(ctx, ep); err != nil {
		return excluded(err)
	}
	// Usage has its own run kind and account; it never becomes a conversation or
	// effect record. Every round is retained rather than only the final stream.
	result := struct {
		TurnID  string             `json:"turn_id"`
		Kind    provider.RunKind   `json:"kind"`
		Account provider.AccountID `json:"account"`
		Clock   time.Time          `json:"clock"`
		Reply   []provider.Item    `json:"reply"`
		Results any                `json:"tool_results"`
		Usage   []provider.Meta    `json:"usage"`
	}{ep.TurnID, provider.Replay, r.route.Request(nil).Credential.Account, ep.Clock, out.Items[len(request.Items):], out.Results, usage}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 524288 {
		return protocol.ReplayResult{}, replayFailure(errors.New("result too large"))
	}
	text := string(raw)
	return protocol.ReplayResult{Output: &text}, nil
}

func (r *conversationRuntime) replayDenied() []string {
	// Startup creates agent views before Boot publishes the listening socket.
	// Read the shared runtime's final configuration rather than a view's copy.
	socket := r.replaySocket
	if r.router != nil {
		socket = r.router.root.replaySocket
	}
	denied := []string{socket}
	for _, name := range []string{"canon", "secrets", "system/core", "versions", "materials", "derived", "staging", "backups"} {
		denied = append(denied, filepath.Join(r.root, name))
	}
	return denied
}

type replayUsageStream struct {
	provider.Stream
	save func(provider.Meta)
}

func (s *replayUsageStream) Close() error { s.save(s.Meta()); return s.Stream.Close() }
