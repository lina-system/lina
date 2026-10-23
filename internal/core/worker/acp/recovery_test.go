// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestRecoverAttemptRestoresBeforePrompt(t *testing.T) {
	for _, mode := range []string{"load", "resume", "fresh", "rejected", "rejected-sign-in"} {
		t.Run(mode, func(t *testing.T) {
			a, ctx, l, spec := fixture(t)
			a.cfg.Environment = append(a.cfg.Environment, "ACP_RECOVERY="+mode)
			if strings.HasPrefix(mode, "rejected") {
				code := "-32603"
				if mode == "rejected-sign-in" {
					code = "-32000"
				}
				a.cfg.Environment = append(a.cfg.Environment, "ACP_ERROR_METHOD=session/load", "ACP_SESSION_ERROR="+code)
			}
			recoverer, ok := any(a).(interface {
				RecoverAttempt(context.Context, lease.Lease, worker.StartSpec, worker.Thread) (worker.Thread, error)
			})
			if !ok {
				t.Fatal("ACP has no settled-attempt recovery entry point")
			}
			prior := worker.Thread{ID: "fixture", TurnID: "acp:1", TaskID: spec.TaskID, AttemptID: "previous", ProcessGeneration: 999, BindingEpoch: 6}
			th, err := recoverer.RecoverAttempt(ctx, l, spec, prior)
			if strings.HasPrefix(mode, "rejected") {
				if err == nil || th.ID != "" {
					t.Fatal("failed load fell back to a new session or prompt")
				}
				if errors.Is(err, worker.ErrNeedsSignIn) {
					t.Fatal("failed restore authorized readiness replacement")
				}
				if _, err = os.Stat(filepath.Join(spec.Workdir, "result.txt")); !os.IsNotExist(err) {
					t.Fatal("failed recovery executed work")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if th.ID != prior.ID || th.TaskID != prior.TaskID || th.AttemptID != spec.AttemptID || th.ProcessGeneration == prior.ProcessGeneration || th.BindingEpoch != 7 || th.TurnID == prior.TurnID {
				t.Fatalf("recovery did not rebind identity: %+v", th)
			}
			for {
				e := event(t, ctx, a)
				if e.Thread != th {
					t.Fatal("recovery event has old binding", e.Thread)
				}
				if e.Progress != nil && strings.Contains(e.Progress.Text, "REPLAY_ONLY") {
					t.Fatal("load history emitted as progress")
				}
				if e.Finish != nil {
					if e.Finish.Status != worker.Done {
						t.Fatal(e.Finish)
					}
					break
				}
			}
			if mode != "fresh" {
				raw, err := os.ReadFile(filepath.Join(spec.Workdir, "restored"))
				if err != nil || string(raw) != "session/"+mode {
					t.Fatalf("restore=%q error=%v", raw, err)
				}
			}
			if a.Result().Output != "Writing result" {
				t.Fatal("replay contaminated result", a.Result())
			}
		})
	}
}

func TestTerminalSnapshotRetainsRecoveryEvidence(t *testing.T) {
	for _, effect := range []string{"none", "tool", "unknown", "replay"} {
		t.Run(effect, func(t *testing.T) {
			a, ctx, l, spec := fixture(t)
			spec.TaskPacket = "cancel"
			th, err := a.Start(ctx, l, spec)
			if err != nil {
				t.Fatal(err)
			}
			for e := event(t, ctx, a); e.Progress == nil || e.Progress.Text != "Waiting"; e = event(t, ctx, a) {
			}
			u := wire.Update{Notification: wire.SessionNotification{SessionID: wire.SessionID(th.ID)}}
			switch effect {
			case "tool":
				u.Notification.Update.ToolCall = &wire.SessionUpdateToolCall{ToolCallID: "effect", Title: "External operation"}
			case "unknown":
				u.Notification.Update.Unrecognized = []byte(`{"sessionUpdate":"future_effect"}`)
			case "replay":
				u.Replay = true
				u.Notification.Update.ToolCall = &wire.SessionUpdateToolCall{ToolCallID: "old", Title: "Old operation"}
			}
			a.update(th, u)
			// Closing the owned transport settles process teardown without a reply.
			a.client.Close()
			f := terminal(t, ctx, a)
			snap, err := a.Snapshot(ctx, th)
			wantKnown := effect == "none" || effect == "replay"
			if err != nil || snap.Finish == nil || *snap.Finish != f || snap.State != worker.Dead || snap.EffectsKnown != wantKnown {
				t.Fatalf("terminal evidence: finish=%+v snapshot=%+v error=%v; effects known want %v", f, snap, err, wantKnown)
			}
			// Snapshots are copies; callers cannot authorize recovery by mutation.
			snap.Finish.Status = worker.Done
			again, err := a.Snapshot(context.Background(), th)
			if err != nil || again.Finish.Status != worker.Failed {
				t.Fatal("snapshot mutation changed terminal evidence")
			}
		})
	}
}

func TestMalformedTransportCannotSettleEffects(t *testing.T) {
	for _, packet := range []string{"malformed", "partial"} {
		t.Run(packet, func(t *testing.T) {
			a, ctx, l, spec := fixture(t)
			spec.TaskPacket = packet
			th, err := a.Start(ctx, l, spec)
			if err != nil {
				t.Fatal(err)
			}
			if terminal(t, ctx, a).Status != worker.Failed {
				t.Fatal("malformed transport did not fail")
			}
			snap, err := a.Snapshot(ctx, th)
			if err != nil || snap.EffectsKnown {
				t.Fatalf("incomplete protocol evidence authorized recovery: %+v %v", snap, err)
			}
		})
	}
}

func TestRestoreKeepsLiveUpdates(t *testing.T) {
	a, ctx, l, spec := fixture(t)
	a.cfg.Environment = append(a.cfg.Environment, "ACP_RECOVERY=load")
	prior := worker.Thread{ID: "fixture", TurnID: "acp:1", TaskID: spec.TaskID, AttemptID: "previous", ProcessGeneration: 999, BindingEpoch: 6}
	th, err := a.RecoverAttempt(ctx, l, spec, prior)
	if err != nil {
		t.Fatal(err)
	}
	live := false
	for {
		e := event(t, ctx, a)
		if e.Thread != th {
			t.Fatal("recovery event has old binding", e.Thread)
		}
		if e.Progress != nil && e.Progress.Phase == "thought" && e.Progress.Text == "LIVE_AFTER_RESTORE" {
			live = true
		}
		if e.Finish != nil {
			if e.Finish.Status != worker.Done {
				t.Fatal(e.Finish)
			}
			break
		}
	}
	if !live {
		t.Fatal("live update after the restore response was dropped")
	}
}
