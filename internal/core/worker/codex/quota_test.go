// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestLN133QuotaReadAndSparseUpdates(t *testing.T) {
	p := newAdapterPeer(t, `{"accountId":"worker-account","rateLimits":{"limitId":"codex","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1800000000},"secondary":{"usedPercent":100,"windowDurationMins":10080}}}`)
	th := p.start(t)
	rows, err := p.s.Quota(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Provider != provider.ChatGPT || rows[0].Account != "worker-account" || rows[0].Window != provider.FiveHour || rows[0].UsedPct == nil || *rows[0].UsedPct != 25 || rows[0].ResetsAt == nil || rows[0].ResetsAt.Unix() != 1800000000 || rows[0].State != provider.QuotaOK || rows[0].Source != provider.Endpoint || rows[0].Observed.IsZero() {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[1].Window != provider.Weekly || rows[1].State != provider.QuotaBlocked || rows[1].ResetsAt != nil {
		t.Fatalf("secondary=%+v", rows[1])
	}
	p.send(frame{Method: "account/rateLimits/updated", Params: json.RawMessage(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":50}}}`)})
	p.snapshot(t, th)
	rows, err = p.s.Quota(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].UsedPct == nil || *rows[0].UsedPct != 50 || rows[0].Window != provider.FiveHour || rows[0].ResetsAt == nil || rows[0].Account != "worker-account" || rows[0].Source != provider.ProviderEvent || rows[1].State != provider.QuotaBlocked {
		t.Fatalf("sparse merge=%+v", rows)
	}
	*rows[0].UsedPct = 999
	again, err := p.s.Quota(p.ctx)
	if err != nil || *again[0].UsedPct != 50 {
		t.Fatal("returned quota aliases cache")
	}
}
func TestLN133QuotaUnknownsAndMultipleLimits(t *testing.T) {
	for _, tc := range []struct {
		payload string
		count   int
	}{
		{`{"rateLimits":{}}`, 1},
		{`{"rateLimits":{"primary":{"usedPercent":0}}}`, 1},
		{`{"rateLimits":{"primary":{"resetsAt":1800000000}}}`, 1},
		{`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":20,"windowDurationMins":300}},"rateLimitsByLimitId":{"codex":{"limitId":"codex","primary":{"usedPercent":20,"windowDurationMins":300}},"review":{"limitId":"review","primary":{"usedPercent":40,"windowDurationMins":1440}}}}`, 2},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			p := newAdapterPeer(t, tc.payload)
			rows, err := p.s.Quota(p.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.count {
				t.Fatalf("rows=%+v", rows)
			}
			if tc.count == 1 {
				if rows[0].Window != provider.UnknownWindow {
					t.Fatalf("inferred window=%s", rows[0].Window)
				}
				var raw struct {
					RateLimits struct{ Primary map[string]json.RawMessage }
				}
				json.Unmarshal([]byte(tc.payload), &raw)
				if _, exists := raw.RateLimits.Primary["usedPercent"]; !exists && (rows[0].UsedPct != nil || rows[0].State != provider.QuotaUnknown) {
					t.Fatal("absent usage became zero")
				}
			}
		})
	}
}

func TestLN133QuotaReadOrdering(t *testing.T) {
	snapshot := `{"rateLimits":{"primary":{"usedPercent":40,"windowDurationMins":300}}}`
	update := frame{Method: "account/rateLimits/updated", Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":60}}}`)}
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_dispatch", true: "after_dispatch"}[during], func(t *testing.T) {
			var after []frame
			if during {
				after = []frame{update}
			}
			p := newAdapterPeerScript(t, snapshot, nil, after)
			th := p.start(t)
			if !during {
				p.send(update)
				p.snapshot(t, th)
			}
			rows, err := p.s.Quota(p.ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := float64(40)
			source := provider.Endpoint
			if during {
				want = 60
				source = provider.ProviderEvent
			}
			if len(rows) != 1 || rows[0].UsedPct == nil || *rows[0].UsedPct != want || rows[0].Source != source {
				t.Fatalf("quota ordering: %+v", rows)
			}
		})
	}
}

func TestLN133QuotaNullUsage(t *testing.T) {
	p := newAdapterPeer(t, `{"rateLimits":{"primary":{"usedPercent":30,"windowDurationMins":300},"individualLimit":{"limit":"100","used":"10","remainingPercent":null,"resetsAt":null}}}`)
	th := p.start(t)
	rows, err := p.s.Quota(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].UsedPct != nil || rows[1].ResetsAt != nil || rows[1].State != provider.QuotaUnknown {
		t.Fatalf("null spend usage=%+v", rows)
	}
	p.send(frame{Method: "account/rateLimits/updated", Params: json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":null}}}`)})
	p.snapshot(t, th)
	rows, err = p.s.Quota(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].UsedPct != nil || rows[0].State != provider.QuotaUnknown || rows[0].Window != provider.FiveHour {
		t.Fatalf("null update retained stale usage=%+v", rows)
	}
}
