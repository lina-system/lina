// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"errors"
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

func TestObserveQuotaFreshnessPerWindow(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	row := func(window provider.Window, state provider.QuotaState, minute int) provider.QuotaRow {
		return provider.QuotaRow{Provider: provider.ChatGPT, Account: "account", Window: window, State: state, Source: provider.ProviderEvent, Observed: base.Add(time.Duration(minute) * time.Minute)}
	}
	weekly := row(provider.Weekly, provider.QuotaOK, 0)
	blocked := row(provider.FiveHour, provider.QuotaBlocked, 1)
	recovered := row(provider.FiveHour, provider.QuotaOK, 2)
	weeklyStop := row(provider.Weekly, provider.QuotaBlocked, 3)
	unknownStop := row(provider.UnknownWindow, provider.QuotaBlocked, 1)
	unknownStop.Source = provider.ErrorCode
	otherAccount := recovered
	otherAccount.Account = "other"
	otherProvider := recovered
	otherProvider.Provider = provider.Claude
	for _, test := range []struct {
		name                string
		prior, update, want []provider.QuotaRow
		eligible            bool
	}{
		{"healthy older window", []provider.QuotaRow{blocked, weekly}, []provider.QuotaRow{recovered, weekly}, []provider.QuotaRow{recovered, weekly}, true},
		{"independent stops", []provider.QuotaRow{blocked, weeklyStop}, []provider.QuotaRow{recovered, weeklyStop}, []provider.QuotaRow{recovered, weeklyStop}, false},
		{"stale sibling", []provider.QuotaRow{blocked, weeklyStop}, []provider.QuotaRow{recovered, weekly}, []provider.QuotaRow{recovered, weeklyStop}, false},
		{"missing blocked window", []provider.QuotaRow{blocked, weekly}, []provider.QuotaRow{row(provider.Weekly, provider.QuotaOK, 2)}, []provider.QuotaRow{row(provider.Weekly, provider.QuotaOK, 2), blocked}, false},
		{"empty snapshot", []provider.QuotaRow{blocked, weekly}, nil, []provider.QuotaRow{blocked, weekly}, false},
		{"equal observation", []provider.QuotaRow{blocked}, []provider.QuotaRow{row(provider.FiveHour, provider.QuotaOK, 1)}, []provider.QuotaRow{blocked}, false},
		{"delayed stop", []provider.QuotaRow{recovered, weekly}, []provider.QuotaRow{blocked, weekly}, []provider.QuotaRow{recovered, weekly}, true},
		{"unknown stop recovery", []provider.QuotaRow{unknownStop, weekly}, []provider.QuotaRow{recovered, weekly}, []provider.QuotaRow{recovered, weekly}, true},
		{"other account", []provider.QuotaRow{blocked}, []provider.QuotaRow{otherAccount}, []provider.QuotaRow{otherAccount, blocked}, false},
		{"other provider", []provider.QuotaRow{blocked}, []provider.QuotaRow{otherProvider}, []provider.QuotaRow{otherProvider, blocked}, false},
		{"unknown stop other account", []provider.QuotaRow{unknownStop}, []provider.QuotaRow{otherAccount}, []provider.QuotaRow{otherAccount, unknownStop}, false},
		{"ambiguous legacy window rows", []provider.QuotaRow{row(provider.FiveHour, provider.QuotaOK, 1), blocked}, []provider.QuotaRow{row(provider.FiveHour, provider.QuotaOK, 1), recovered}, []provider.QuotaRow{row(provider.FiveHour, provider.QuotaOK, 1), recovered, row(provider.FiveHour, provider.QuotaOK, 1), blocked}, false},
		{"initial unknown replaced", []provider.QuotaRow{row(provider.UnknownWindow, provider.QuotaUnknown, 0)}, []provider.QuotaRow{recovered, weekly}, []provider.QuotaRow{recovered, weekly}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{cfg: Config{DB: store(t)}}
			for _, rows := range [][]provider.QuotaRow{test.prior, test.update} {
				original := append([]provider.QuotaRow(nil), rows...)
				if err := s.ObserveQuota(t.Context(), "codex", rows); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(rows, original) {
					t.Fatal("mutated caller's quota rows")
				}
			}
			restarted := &Service{cfg: s.cfg}
			got, err := restarted.quota(t.Context(), "codex")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("persisted quota = %+v; want %+v", got, test.want)
			}
			chosen := work.ChooseWorker([]work.WorkerCandidate{{Name: "codex", Slots: 1, Quota: got}}, nil, nil, nil, base.Add(time.Hour))
			if (chosen != "") != test.eligible {
				t.Errorf("chosen = %q; eligible = %t", chosen, test.eligible)
			}
		})
	}
}

func TestUnidentifiedWorkerStopRecovery(t *testing.T) {
	for _, test := range []struct {
		name, worker string
		age          time.Duration
		state        provider.QuotaState
		empty        bool
		readError    error
		eligible     bool
	}{
		{name: "fresh account", worker: "codex", age: time.Second, state: provider.QuotaOK, eligible: true},
		{name: "failed endpoint", worker: "codex", age: time.Second, state: provider.QuotaOK, readError: errors.New("quota unavailable"), eligible: true},
		{name: "older snapshot", worker: "codex", age: -time.Second, state: provider.QuotaOK},
		{name: "equal snapshot", worker: "codex", state: provider.QuotaOK},
		{name: "other worker", worker: "antigravity", age: time.Second, state: provider.QuotaOK},
		{name: "empty snapshot", worker: "codex", empty: true},
		{name: "fresh blocked account", worker: "codex", age: time.Second, state: provider.QuotaBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{cfg: Config{DB: store(t)}}
			// No cached quota and no endpoint rows: park can only identify the worker.
			if err := s.park(t.Context(), &quotaAdapter{name: "codex", err: test.readError}); err != nil {
				t.Fatal(err)
			}
			prior, err := s.quota(t.Context(), "codex")
			if err != nil || len(prior) != 1 || prior[0].Provider != "" || prior[0].Account != "" || prior[0].State != provider.QuotaBlocked {
				t.Fatalf("unidentified stop: %+v %v", prior, err)
			}
			restarted := &Service{cfg: s.cfg}
			rows := []provider.QuotaRow{{Provider: provider.ChatGPT, Account: "restored", Window: provider.FiveHour, State: test.state, Source: provider.Endpoint, Observed: prior[0].Observed.Add(test.age)}}
			if test.empty {
				rows = nil
			}
			if err := restarted.ObserveQuota(t.Context(), test.worker, rows); err != nil {
				t.Fatal(err)
			}
			got, err := restarted.quota(t.Context(), "codex")
			if err != nil {
				t.Fatal(err)
			}
			chosen := work.ChooseWorker([]work.WorkerCandidate{{Name: "codex", Slots: 1, Quota: got}}, nil, nil, nil, time.Now())
			if (chosen != "") != test.eligible {
				t.Fatalf("chosen=%q eligible=%t rows=%+v", chosen, test.eligible, got)
			}
		})
	}
}

func TestObserveQuotaBucketIdentity(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	row := func(limit, bucket string, window provider.Window, state provider.QuotaState, minute int) provider.QuotaRow {
		return provider.QuotaRow{Provider: provider.ChatGPT, Account: "account", LimitID: limit, Bucket: bucket, Window: window, State: state, Source: provider.Endpoint, Observed: base.Add(time.Duration(minute) * time.Minute)}
	}
	blocked := row("codex", "primary", provider.UnknownWindow, provider.QuotaBlocked, 1)
	recovered := row("codex", "primary", provider.FiveHour, provider.QuotaOK, 2)
	sibling := row("codex", "secondary", provider.FiveHour, provider.QuotaBlocked, 1)
	other := row("review", "primary", provider.FiveHour, provider.QuotaBlocked, 1)
	unmeasured := row("codex", "primary", provider.UnknownWindow, provider.QuotaUnknown, 1)
	otherAccount := recovered
	otherAccount.Account = "other"
	otherProvider := recovered
	otherProvider.Provider = provider.Claude
	stale := recovered
	stale.Observed = base
	legacy := blocked
	legacy.LimitID, legacy.Bucket = "", ""
	legacyKnown := legacy
	legacyKnown.Window = provider.FiveHour
	legacyRecovery := recovered
	legacyRecovery.LimitID, legacyRecovery.Bucket = "", ""
	legacyUnknownRecovery := legacyRecovery
	legacyUnknownRecovery.Window = provider.UnknownWindow
	inferredMeasurement := blocked
	inferredMeasurement.Source = provider.ErrorCode
	used := 100.0
	legacyInferredMeasurement := legacy
	legacyInferredMeasurement.Source, legacyInferredMeasurement.UsedPct = provider.ErrorCode, &used
	for _, test := range []struct {
		name                string
		prior, update, want []provider.QuotaRow
		eligible            bool
	}{
		{"duration discovered", []provider.QuotaRow{blocked}, []provider.QuotaRow{recovered}, []provider.QuotaRow{recovered}, true},
		{"stale duration", []provider.QuotaRow{blocked}, []provider.QuotaRow{stale}, []provider.QuotaRow{blocked}, false},
		{"sibling cannot recover", []provider.QuotaRow{blocked}, []provider.QuotaRow{sibling}, []provider.QuotaRow{sibling, blocked}, false},
		{"same duration different buckets", []provider.QuotaRow{recovered, sibling}, []provider.QuotaRow{recovered}, []provider.QuotaRow{recovered, sibling}, false},
		{"same bucket different limits", []provider.QuotaRow{blocked, other}, []provider.QuotaRow{recovered}, []provider.QuotaRow{recovered, other}, false},
		{"different account", []provider.QuotaRow{blocked}, []provider.QuotaRow{otherAccount}, []provider.QuotaRow{otherAccount, blocked}, false},
		{"different provider", []provider.QuotaRow{blocked}, []provider.QuotaRow{otherProvider}, []provider.QuotaRow{otherProvider, blocked}, false},
		{"identified error retains bucket", []provider.QuotaRow{inferredMeasurement}, []provider.QuotaRow{row("codex", "secondary", provider.Weekly, provider.QuotaOK, 2)}, []provider.QuotaRow{row("codex", "secondary", provider.Weekly, provider.QuotaOK, 2), inferredMeasurement}, false},
		{"unmeasured sibling retains identity", []provider.QuotaRow{unmeasured}, []provider.QuotaRow{row("codex", "secondary", provider.Weekly, provider.QuotaOK, 2)}, []provider.QuotaRow{row("codex", "secondary", provider.Weekly, provider.QuotaOK, 2), unmeasured}, true},
		{"legacy unknown cannot infer bucket", []provider.QuotaRow{legacy}, []provider.QuotaRow{recovered}, []provider.QuotaRow{recovered, legacy}, false},
		{"legacy known cannot infer bucket", []provider.QuotaRow{legacyKnown}, []provider.QuotaRow{recovered}, []provider.QuotaRow{recovered, legacyKnown}, false},
		{"legacy measured error remains", []provider.QuotaRow{legacyInferredMeasurement}, []provider.QuotaRow{recovered}, []provider.QuotaRow{recovered, legacyInferredMeasurement}, false},
		{"legacy unique window", []provider.QuotaRow{legacyKnown}, []provider.QuotaRow{legacyRecovery}, []provider.QuotaRow{legacyRecovery}, true},
		{"legacy unknown lacks identity", []provider.QuotaRow{legacy}, []provider.QuotaRow{legacyUnknownRecovery}, []provider.QuotaRow{legacyUnknownRecovery, legacy}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{cfg: Config{DB: store(t)}}
			if err := s.ObserveQuota(t.Context(), "codex", test.prior); err != nil {
				t.Fatal(err)
			}
			// Persist before the metadata changes, then recover using a fresh service.
			restarted := &Service{cfg: s.cfg}
			original := append([]provider.QuotaRow(nil), test.update...)
			if err := restarted.ObserveQuota(t.Context(), "codex", test.update); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, test.update) {
				t.Fatal("mutated caller rows")
			}
			got, err := restarted.quota(t.Context(), "codex")
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("quota=%+v want=%+v err=%v", got, test.want, err)
			}
			chosen := work.ChooseWorker([]work.WorkerCandidate{{Name: "codex", Slots: 1, Quota: got}}, nil, nil, nil, base.Add(time.Hour))
			if (chosen != "") != test.eligible {
				t.Fatalf("chosen=%q eligible=%t", chosen, test.eligible)
			}
		})
	}
}
