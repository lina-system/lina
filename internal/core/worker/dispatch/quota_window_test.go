// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestParkPreservesIndependentQuotaWindows(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, state := range []provider.QuotaState{provider.QuotaOK, provider.QuotaWarning, provider.QuotaUnknown, provider.QuotaBlocked} {
			name := string(state)
			if cached {
				name += "-cached"
			}
			t.Run(name, func(t *testing.T) {
				s := &Service{cfg: Config{DB: store(t)}}
				now := time.Now()
				shortReset, weeklyReset := now.Add(time.Hour), now.Add(7*24*time.Hour)
				used := 25.0
				rows := []provider.QuotaRow{
					{Provider: provider.ChatGPT, Account: "account", Window: provider.FiveHour, State: provider.QuotaBlocked, ResetsAt: &shortReset, Source: provider.ProviderEvent, Observed: now},
					{Provider: provider.ChatGPT, Account: "account", Window: provider.Weekly, State: state, UsedPct: &used, ResetsAt: &weeklyReset, Source: provider.Endpoint, Observed: now},
				}
				original := append([]provider.QuotaRow(nil), rows...)
				a := &quotaAdapter{name: "codex", rows: rows}
				if cached {
					if err := s.ObserveQuota(t.Context(), a.name, rows); err != nil {
						t.Fatal(err)
					}
					a.rows = nil
				}
				if err := s.park(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				// Read using a fresh service to cover the durable choice after restart.
				restarted := &Service{cfg: s.cfg}
				got, err := restarted.quota(t.Context(), a.name)
				if err != nil || len(got) != 2 {
					t.Fatalf("quota rows: %+v %v", got, err)
				}
				candidate := []work.WorkerCandidate{{Name: a.name, Slots: 1, Quota: got}}
				if chosen := work.ChooseWorker(candidate, nil, nil, nil, now); chosen != "" {
					t.Error("worker chosen before exhausted window reset")
				}
				want := a.name
				if state == provider.QuotaBlocked {
					want = ""
				}
				if chosen := work.ChooseWorker(candidate, nil, nil, nil, shortReset); chosen != want {
					t.Errorf("at five-hour reset: chosen=%q want=%q; weekly state=%q", chosen, want, got[1].State)
				}
				if chosen := work.ChooseWorker(candidate, nil, nil, nil, weeklyReset); chosen != a.name {
					t.Error("worker unavailable after all exhausted windows reset")
				}
				if got[1].State != state || !got[1].ResetsAt.Equal(weeklyReset) || *got[1].UsedPct != used {
					t.Errorf("independent weekly measurement changed: %+v", got[1])
				}
				if state != provider.QuotaBlocked && got[1].Source != provider.Endpoint {
					t.Error("healthy window attributed to quota error")
				}
				if !reflect.DeepEqual(rows, original) {
					t.Error("parking mutated adapter-owned rows")
				}
			})
		}
	}
}

func TestParkWithoutExhaustedWindowPreservesMeasurements(t *testing.T) {
	s := &Service{cfg: Config{DB: store(t)}}
	reset := time.Now().Add(time.Hour)
	a := &quotaAdapter{name: "codex", rows: []provider.QuotaRow{{Provider: provider.ChatGPT, Account: "account", Window: provider.Weekly, State: provider.QuotaOK, ResetsAt: &reset, Source: provider.Endpoint}}}
	if err := s.park(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	got, err := s.quota(t.Context(), a.name)
	if err != nil || len(got) != 2 {
		t.Fatalf("expected reported window and unknown stop: %+v %v", got, err)
	}
	if got[0].State != provider.QuotaOK || got[1].State != provider.QuotaBlocked || got[1].Window != provider.UnknownWindow || got[1].ResetsAt != nil {
		t.Fatalf("stop invented an exhausted window or reset: %+v", got)
	}
	if chosen := work.ChooseWorker([]work.WorkerCandidate{{Name: a.name, Slots: 1, Quota: got}}, nil, nil, nil, reset); chosen != "" {
		t.Fatal("healthy reset cleared a stop with no known reset")
	}
}
