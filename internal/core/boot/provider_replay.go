// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func (r *conversationRuntime) providerReplayAllowed(p protocol.ReplayRequest) bool {
	return os.Getenv("LINA_QA_REAL_MODEL") == "1" && p.Provider != nil && r.route != nil && string(r.route.Adapter.Provider()) == *p.Provider
}
func providerReportOutput(report replay.ProviderReport, err error) (protocol.ReplayResult, error) {
	if err != nil {
		report.Failure = "replay_failed"
		var exclusion replay.Exclusion
		if errors.As(err, &exclusion) {
			report.Failure = string(exclusion)
		}
		if errors.Is(err, replay.ErrExcluded) {
			report.Failure = "recorded_result_unavailable"
		}
	}
	raw, e := json.Marshal(report)
	if e != nil || len(raw) > 524288 {
		return protocol.ReplayResult{}, replayFailure(replay.ErrInvalid)
	}
	text := string(raw)
	return protocol.ReplayResult{Output: &text}, nil
}
func replaySourceDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Synthetic inputs never read canon. All tool calls are matched against canned
// results in the existing sandbox worker, with no live tool implementations.
func (r *conversationRuntime) providerFixture(ctx context.Context, s clientsrv.Session, p protocol.ReplayRequest) (protocol.ReplayResult, error) {
	if !r.providerReplayAllowed(p) || p.Fixture == nil || p.TurnID != nil || p.Episode != nil || p.Candidates != nil {
		return protocol.ReplayResult{}, replayFailure(replay.ErrInvalid)
	}
	f, err := replay.ParseProviderFixture(*p.Fixture)
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	model := r.route.Model
	if p.Model != nil {
		model = provider.ModelRef(*p.Model)
	}
	effort := provider.Effort("")
	if p.Effort != nil {
		effort = provider.Effort(*p.Effort)
	}
	report := replay.ProviderReport{Version: 1, Provider: r.route.Adapter.Provider(), Route: r.route.Route, Model: model, Source: replaySourceDigest(*p.Fixture), Requests: []replay.RequestReport{}}
	activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "provider replay"})
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	defer activity.Release()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return protocol.ReplayResult{}, replayFailure(err)
	}
	history := append([]provider.Item(nil), f.History...)
	// Fresh per run, stable across its turns, with no installation identity.
	conversation := rand.Text()
	previous := len(history) - 1
	var first provider.Request
	for i, turn := range f.Turns {
		request, assembly := assemble.Assemble(assemble.Input{Core: assemble.PersonaCoreV1 + "\n" + assemble.TUIFormatting, MainID: "provider-replay", AgentID: "lina", ConversationID: conversation, AgentName: "Lina", OwnerPersonID: "public-reader", Persona: f.Persona, PersonaRevision: "public-v1", AlwaysTools: f.Tools, History: history, PreviousInput: &previous, Current: turn.Input, Packet: assemble.ContextPacket{{ID: "public-context", Kind: assemble.Clock, Text: turn.Context}}, Model: model, Effort: effort, MaxTokens: 1000000}, conversationRequestSize)
		if assembly.Failure != "" || len(assembly.Dropped) > 0 {
			return providerReportOutput(report, replay.ErrInvalid)
		}
		request.Purpose = provider.Replay
		if i == 0 {
			first = request
		}
		round := 0
		out, err := replay.Sandboxed(ctx, replay.SandboxConfig{Executable: executable, Denied: r.replayDenied(), Environment: []string{"PATH=" + os.Getenv("PATH")}, Stream: func(ctx context.Context, q provider.Request) (provider.Stream, error) {
			round++
			if round > 16 {
				return nil, replay.ErrInvalid
			}
			q.Layout = request.Layout.Clone()
			if len(q.Items) > len(request.Items) {
				q.Layout.Newest = &provider.Position{Items: len(q.Items), Tools: len(q.Tools)}
			}
			q, err = r.selectProviderCapabilities(q)
			if err != nil {
				return nil, err
			}
			q.Credential = r.route.Request(nil).Credential
			return report.Observe(fmt.Sprint(i+1), round, r.route.Route, time.Now, func() (provider.Stream, error) { return r.route.Adapter.Stream(ctx, activity, q) }, &q.Bridge), nil
		}}, replay.WorkerInput{RuntimeTools: r.route.Adapter.Class() == provider.AgentRuntime, Request: request, Calls: turn.Calls})
		if err != nil {
			return providerReportOutput(report, err)
		}
		if len(out.Results) != len(turn.Calls) {
			return providerReportOutput(report, errors.New("fixture_tool_loop_missing"))
		}
		for _, result := range out.Results {
			if result.Code != "ok" {
				return providerReportOutput(report, replay.ErrInvalid)
			}
		}
		// ContextPacket is ephemeral. Keep only current input and actual model/tool
		// output, preserving opaque reasoning bytes across the next live turn.
		previous = len(history) + 1
		history = append(history, turn.Input)
		history = append(history, out.Items[len(request.Items):]...)
	}
	// One summary request in the shape Core sends, so a report carries its plain
	// tokens beside the conversation's. It reuses the first turn's fixed prefix.
	if f.Summary != nil {
		base, err := r.selectProviderCapabilities(first)
		if err != nil {
			return providerReportOutput(report, err)
		}
		fixed := base.Layout.Fixed.Items
		if fixed <= 0 || fixed > len(base.Items) {
			return providerReportOutput(report, replay.ErrInvalid)
		}
		segment := compaction.PassSegment{}
		for i, item := range f.History[:f.Summary.Segment] {
			var m struct{ Role string }
			if json.Unmarshal(item, &m) != nil {
				return providerReportOutput(report, replay.ErrInvalid)
			}
			segment.Records = append(segment.Records, compaction.Record{ID: fmt.Sprintf("history-%d", i), Item: item, User: m.Role == "user"})
		}
		aux := compaction.Auxiliary{RetainPrefix: true, Request: provider.Request{Model: base.Model, Effort: base.Effort, Instructions: base.Instructions, Items: base.Items[:fixed], Tools: base.Tools, Fields: base.Fields, Layout: base.Layout, CacheIdentity: base.CacheIdentity, Capabilities: base.Capabilities}}
		summary, err := aux.SummaryRequest(segment)
		if err != nil {
			return providerReportOutput(report, err)
		}
		summary.Credential = r.route.Request(nil).Credential
		stream := report.Observe("summary", 1, r.route.Route, time.Now, func() (provider.Stream, error) {
			return r.route.Adapter.Stream(ctx, activity, summary)
		})
		for {
			event, err := stream.Next(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				stream.Close()
				return providerReportOutput(report, err)
			}
			// A tool call is not a summary. It stays in the measurement as a
			// failed row rather than running anything.
			if event.Kind == provider.ToolCallReady || event.Kind == provider.Terminal {
				break
			}
		}
		if err := stream.Close(); err != nil {
			return providerReportOutput(report, err)
		}
	}
	return providerReportOutput(report, nil)
}
