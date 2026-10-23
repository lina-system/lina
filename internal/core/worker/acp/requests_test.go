// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func pendingRequest(t *testing.T, ctx context.Context, a *Adapter) worker.Request {
	t.Helper()
	for {
		e := event(t, ctx, a)
		if e.Finish != nil {
			t.Fatalf("request became terminal: %+v", e.Finish)
		}
		if e.Approval != nil {
			return *e.Approval
		}
		if e.Question != nil {
			return *e.Question
		}
	}
}

func TestPermissionAnswersAreOnceOnly(t *testing.T) {
	for _, option := range []string{"one-allow", "one-deny"} {
		t.Run(option, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			s.TaskPacket = "permission"
			th, err := a.Start(ctx, l, s)
			if err != nil {
				t.Fatal(err)
			}
			r := pendingRequest(t, ctx, a)
			if r.Method != "session/request_permission" {
				t.Fatal(r)
			}
			if _, err := os.Stat(filepath.Join(s.Workdir, "result.txt")); !os.IsNotExist(err) {
				t.Fatal("file before approval", err)
			}
			snap, err := a.Snapshot(ctx, th)
			if err != nil || snap.State != worker.Waiting || len(snap.Pending) != 1 {
				t.Fatalf("pending snapshot: %+v %v", snap, err)
			}
			answer := func(id string) worker.Answer {
				return worker.Answer{RequestID: r.ID, Result: json.RawMessage(`{"outcome":{"outcome":"selected","optionId":"` + id + `"}}`)}
			}
			for _, bad := range []string{"persist-allow", "persist-deny", "invented"} {
				if err := a.Answer(ctx, th, answer(bad)); err == nil {
					t.Fatal("unsafe option sent", bad)
				}
			}
			stale := th
			stale.BindingEpoch++
			if err := a.Answer(ctx, stale, answer(option)); err == nil {
				t.Fatal("foreign answer sent")
			}
			if err := a.Answer(ctx, th, answer(option)); err != nil {
				t.Fatal(err)
			}
			if err := a.Answer(ctx, th, answer(option)); err == nil {
				t.Fatal("duplicate answer sent")
			}
			if f := terminal(t, ctx, a); f.Status != worker.Done {
				t.Fatal(f)
			}
			data, err := os.ReadFile(filepath.Join(s.Workdir, "answer.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Outcome struct{ OptionID string } }
			if json.Unmarshal(data, &got) != nil || got.Outcome.OptionID != option {
				t.Fatalf("answer %s", data)
			}
			_, err = os.Stat(filepath.Join(s.Workdir, "result.txt"))
			if (option == "one-allow" && err != nil) || (option == "one-deny" && !os.IsNotExist(err)) {
				t.Fatal("wrong file effect", err)
			}
		})
	}
}

func TestFormAnswerAndPendingCancel(t *testing.T) {
	for _, mode := range []string{"question", "permission"} {
		t.Run(mode, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			s.TaskPacket = mode
			th, err := a.Start(ctx, l, s)
			if err != nil {
				t.Fatal(err)
			}
			r := pendingRequest(t, ctx, a)
			answer := worker.Answer{RequestID: r.ID, Result: json.RawMessage(`{"action":"accept","content":{"name":"result"}}`)}
			if mode == "question" {
				for _, bad := range []string{`{"action":"accept","content":{}}`, `{"action":"accept","content":{"name":false}}`, `{"action":"accept","content":{"name":"ok","unexpected":"value"}}`} {
					if err := a.Answer(ctx, th, worker.Answer{RequestID: r.ID, Result: json.RawMessage(bad)}); err == nil {
						t.Fatal("invalid form accepted", bad)
					}
				}
				if err := a.Answer(ctx, th, answer); err != nil {
					t.Fatal(err)
				}
				if f := terminal(t, ctx, a); f.Status != worker.Done {
					t.Fatal(f)
				}
				data, err := os.ReadFile(filepath.Join(s.Workdir, "answer.json"))
				if err != nil || string(data) != string(answer.Result) {
					t.Fatalf("form answer: %s %v", data, err)
				}
			} else {
				if err := a.Interrupt(ctx, th, worker.UserCancel); err != nil {
					t.Fatal(err)
				}
				if err := a.Answer(ctx, th, answer); err == nil {
					t.Fatal("answer after cancel")
				}
				for {
					e := event(t, ctx, a)
					if e.Progress != nil && e.Progress.Text == "Cancel received" {
						break
					}
				}
				data, err := os.ReadFile(filepath.Join(s.Workdir, "answer.json"))
				if err != nil {
					t.Fatal(err)
				}
				var got struct{ Outcome struct{ Outcome string } }
				if json.Unmarshal(data, &got) != nil || got.Outcome.Outcome != "cancelled" {
					t.Fatalf("cancel response: %s", data)
				}
				if err := os.WriteFile(filepath.Join(s.Workdir, "release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if f := terminal(t, ctx, a); f.Status != worker.Interrupted {
					t.Fatal(f)
				}
			}
			snap, err := a.Snapshot(ctx, th)
			if err != nil || len(snap.Pending) != 0 {
				t.Fatal("pending after turn", snap, err)
			}
		})
	}
}

func TestPermissionReplyNeverRemembersAChoice(t *testing.T) {
	for _, tc := range []struct {
		options string
		allow   bool
		want    string
	}{
		{`[{"kind":"allow_always","optionId":"always"},{"kind":"allow_once","optionId":"once"}]`, true, "once"},
		{`[{"kind":"reject_always","optionId":"always"},{"kind":"reject_once","optionId":"deny"}]`, false, "deny"},
		{`[{"kind":"allow_always","optionId":"always"}]`, true, ""},
		{`[{"kind":"reject_always","optionId":"always"}]`, false, ""},
		{`[{"kind":"allow_once","optionId":"same"},{"kind":"allow_always","optionId":"same"}]`, true, ""},
	} {
		raw, err := PermissionReply(json.RawMessage(`{"options":`+tc.options+`}`), tc.allow)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Outcome struct{ Outcome, OptionID string }
		}
		if json.Unmarshal(raw, &got) != nil || got.Outcome.OptionID != tc.want || (tc.want == "" && got.Outcome.Outcome != "cancelled") {
			t.Fatalf("unsafe option: %s", raw)
		}
	}
}
