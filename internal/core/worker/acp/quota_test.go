// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/provider"
)

const failureAdvertisement = `{"jetbrains":{"air":{"version":1,"capabilities":["sessionFailure"]}}}`

func failureUpdate(category, actions, severity string) string {
	return fmt.Sprintf(`{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"incident","revision":1,"category":%q,"severity":%q,"actions":%s,"title":"private-account-detail"}}}}}`, category, severity, actions)
}

func TestPromptRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		code    string
		failure worker.FailureKind
	}{{"-32000", worker.LoginFailure}, {"-32603", worker.WorkerFailure}} {
		t.Run(tc.code, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			a.cfg.Environment = append(a.cfg.Environment, "ACP_ERROR_METHOD=session/prompt", "ACP_SESSION_ERROR="+tc.code)
			if _, err := a.Start(ctx, l, s); err != nil {
				t.Fatal(err)
			}
			f := terminal(t, ctx, a)
			if f.Status != worker.Failed || f.Failure != tc.failure {
				t.Fatalf("RPC %s: %+v", tc.code, f)
			}
		})
	}
}

func TestSessionFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, category, actions, severity, stop string
		advertised                              bool
		failure                                 worker.FailureKind
	}{
		{"rate limited", "limit", `["retry"]`, "error", "end_turn", true, worker.RateLimitFailure},
		{"quota exhausted", "limit", `[]`, "error", "end_turn", true, worker.QuotaFailure},
		{"sign in", "access", `["login"]`, "error", "end_turn", true, worker.LoginFailure},
		{"connection", "connection", `["new_session"]`, "error", "end_turn", true, worker.ConnectionFailure},
		{"warning", "limit", `["retry"]`, "warning", "end_turn", true, ""},
		{"not advertised", "limit", `[]`, "error", "end_turn", false, ""},
		{"unknown category", "future", `[]`, "error", "end_turn", true, ""},
		{"missing actions", "limit", `null`, "error", "end_turn", true, ""},
		{"stop precedence", "limit", `[]`, "error", "refusal", true, worker.WorkerFailure},
		{"cancel precedence", "limit", `[]`, "error", "cancelled", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			s.TaskPacket = tc.stop
			if tc.advertised {
				a.cfg.Environment = append(a.cfg.Environment, "ACP_INIT_META="+failureAdvertisement)
			}
			a.cfg.Environment = append(a.cfg.Environment, "ACP_UPDATES=["+failureUpdate(tc.category, tc.actions, tc.severity)+"]")
			if _, err := a.Start(ctx, l, s); err != nil {
				t.Fatal(err)
			}
			f := terminal(t, ctx, a)
			if f.Failure != tc.failure {
				t.Fatalf("finish: %+v, want %s", f, tc.failure)
			}
			rows, err := a.Quota(ctx)
			if err != nil {
				t.Fatal(err)
			}
			blocked := tc.advertised && tc.severity == "error" && tc.category == "limit" && tc.actions != "null"
			if blocked {
				if len(rows) != 1 || rows[0].State != provider.QuotaBlocked || rows[0].Window != provider.UnknownWindow || rows[0].ResetsAt != nil {
					t.Fatalf("quota: %+v", rows)
				}
			} else if len(rows) != 0 {
				t.Fatalf("invented quota: %+v", rows)
			}
		})
	}
}

func TestVendorQuotaMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, key string
		window              provider.Window
		state               provider.QuotaState
		pct                 *float64
		reset               bool
		count               int
	}{
		{"reported", `{"status":"rejected","rateLimitType":"five_hour","utilization":0.8,"resetsAt":2000000000}`, "_claude/rateLimit", provider.FiveHour, provider.QuotaBlocked, ptrFloat(80), true, 1},
		{"absent values", `{"status":"allowed"}`, "_claude/rateLimit", provider.UnknownWindow, provider.QuotaOK, nil, false, 1},
		{"weekly warning", `{"status":"allowed_warning","rateLimitType":"seven_day","utilization":0.9}`, "_claude/rateLimit", provider.Weekly, provider.QuotaWarning, ptrFloat(90), false, 1},
		{"unknown window", `{"status":"future","rateLimitType":"future"}`, "_claude/rateLimit", provider.UnknownWindow, provider.QuotaUnknown, nil, false, 1},
		{"unconfigured", `{"status":"rejected"}`, "", "", "", nil, false, 0},
		{"empty", `{}`, "_claude/rateLimit", "", "", nil, false, 0},
		{"null", `null`, "_claude/rateLimit", "", "", nil, false, 0},
		{"malformed", `{"status":42}`, "_claude/rateLimit", "", "", nil, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			a.cfg.Profile.RateLimitKey = tc.key
			a.cfg.Environment = append(a.cfg.Environment, `ACP_UPDATES=[{"sessionUpdate":"usage_update","used":12,"size":100,"_meta":{"_claude/rateLimit":`+tc.metadata+`}}]`)
			if _, err := a.Start(ctx, l, s); err != nil {
				t.Fatal(err)
			}
			if f := terminal(t, ctx, a); f.Status != worker.Done {
				t.Fatal(f)
			}
			rows, err := a.Quota(ctx)
			if err != nil || len(rows) != tc.count {
				t.Fatalf("quota %+v %v; want %d", rows, err, tc.count)
			}
			if len(rows) == 0 {
				return
			}
			r := rows[0]
			if r.Window != tc.window || r.State != tc.state || r.Source != provider.ProviderEvent || r.Observed.IsZero() {
				t.Fatalf("row: %+v", r)
			}
			if (r.UsedPct == nil) != (tc.pct == nil) || r.UsedPct != nil && *r.UsedPct != *tc.pct {
				t.Fatalf("used: %v", r.UsedPct)
			}
			if (r.ResetsAt != nil) != tc.reset || r.ResetsAt != nil && !r.ResetsAt.Equal(time.Unix(2000000000, 0)) {
				t.Fatalf("reset: %v", r.ResetsAt)
			}
		})
	}
}
func ptrFloat(v float64) *float64 { return &v }

func TestFailureKeepsReportedQuotaWindows(t *testing.T) {
	a, ctx, l, s := fixture(t)
	a.cfg.Profile.RateLimitKey = "_claude/rateLimit"
	a.cfg.Environment = append(a.cfg.Environment, "ACP_INIT_META="+failureAdvertisement,
		`ACP_UPDATES=[{"sessionUpdate":"usage_update","_meta":{"_claude/rateLimit":{"status":"rejected","rateLimitType":"five_hour","resetsAt":2000000000}}},{"sessionUpdate":"usage_update","_meta":{"_claude/rateLimit":{"status":"allowed","rateLimitType":"seven_day","resetsAt":2000100000}}},`+failureUpdate("limit", `[]`, "error")+`]`)
	th, err := a.Start(ctx, l, s)
	if err != nil {
		t.Fatal(err)
	}
	if f := terminal(t, ctx, a); f.Failure != worker.QuotaFailure {
		t.Fatal(f)
	}
	rows, err := a.Quota(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("quota: %+v %v", rows, err)
	}
	if rows[0].State != provider.QuotaBlocked || rows[0].Window != provider.FiveHour || rows[1].State != provider.QuotaOK || rows[1].Window != provider.Weekly {
		t.Fatalf("quota: %+v", rows)
	}
	*rows[0].ResetsAt = time.Time{}
	again, err := a.Quota(ctx)
	if err != nil || again[0].ResetsAt.IsZero() {
		t.Fatal("quota snapshot aliases cached observations")
	}
	snap, err := a.Snapshot(ctx, th)
	if err != nil || snap.Finish == nil || snap.Finish.Failure != worker.QuotaFailure || !snap.EffectsKnown {
		t.Fatalf("replacement cannot reconcile: %+v %v", snap, err)
	}
	if err = a.Input(ctx, th, worker.Input{Text: "cancel"}); err != nil {
		t.Fatal(err)
	}
	snap, err = a.Snapshot(ctx, th)
	if err != nil || snap.Finish != nil || snap.EffectsKnown {
		t.Fatalf("prior finish leaked into next turn: %+v %v", snap, err)
	}
}

func TestFailureAdvertisementVersionAndScope(t *testing.T) {
	for _, tc := range []struct{ name, meta string }{
		{"future version", `{"jetbrains":{"air":{"version":2,"capabilities":["sessionFailure"]}}}`},
		{"other capability", `{"jetbrains":{"air":{"version":1,"capabilities":["future"]}}}`},
		{"wrong namespace", `{"sessionFailure":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			a.cfg.Environment = append(a.cfg.Environment, "ACP_INIT_META="+tc.meta, "ACP_UPDATES=["+failureUpdate("limit", `[]`, "error")+"]")
			if _, err := a.Start(ctx, l, s); err != nil {
				t.Fatal(err)
			}
			if f := terminal(t, ctx, a); f.Status != worker.Done {
				t.Fatal(f)
			}
			if rows, err := a.Quota(ctx); err != nil || len(rows) != 0 {
				t.Fatalf("invented quota: %+v %v", rows, err)
			}
		})
	}
}

func TestNativeToolOutcomeFencesReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, updates string
		known         bool
	}{
		{"missing outcome", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write"}`, false},
		{"running", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write","status":"in_progress"}`, false},
		{"completed", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write"},{"sessionUpdate":"tool_call_update","toolCallId":"write","status":"completed"}`, true},
		{"failed", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write"},{"sessionUpdate":"tool_call_update","toolCallId":"write","status":"failed"}`, true},
		{"unknown status", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write","status":"future"}`, false},
		{"update only", `{"sessionUpdate":"tool_call_update","toolCallId":"write","title":"Write"}`, false},
		{"title after outcome", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write","status":"completed"},{"sessionUpdate":"tool_call_update","toolCallId":"write","title":"Done"}`, true},
		{"one of two pending", `{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write"},{"sessionUpdate":"tool_call","toolCallId":"read","title":"Read","status":"completed"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			a.cfg.Environment = append(a.cfg.Environment, "ACP_INIT_META="+failureAdvertisement, "ACP_UPDATES=["+tc.updates+","+failureUpdate("limit", `[]`, "error")+"]")
			th, err := a.Start(ctx, l, s)
			if err != nil {
				t.Fatal(err)
			}
			if f := terminal(t, ctx, a); f.Failure != worker.QuotaFailure {
				t.Fatal(f)
			}
			snap, err := a.Snapshot(ctx, th)
			if err != nil || snap.EffectsKnown != tc.known {
				t.Fatalf("effects known=%v want=%v: %v", snap.EffectsKnown, tc.known, err)
			}
		})
	}
}
