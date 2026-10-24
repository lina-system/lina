// SPDX-License-Identifier: Apache-2.0
package work

import (
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
	"time"
)

func TestChooserQuotaAndSituation(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	reset := now.Add(time.Minute)
	candidates := []WorkerCandidate{
		{Name: "blocked", Slots: 2, Quota: []provider.QuotaRow{{State: provider.QuotaBlocked, ResetsAt: &reset}}},
		{Name: "unknown", Slots: 2},
		{Name: "ok", Slots: 1, Quota: []provider.QuotaRow{{State: provider.QuotaOK}}},
	}
	rules := []ChoiceRule{{Worker: "unknown", Tags: []string{"backend"}, Priority: 10}}
	if got := ChooseWorker(candidates, rules, []string{"backend"}, nil, now); got != "ok" {
		t.Fatalf("measured quota should win: %s", got)
	}
	if got := ChooseWorker(candidates, rules, []string{"backend"}, []string{"ok"}, now); got != "unknown" {
		t.Fatalf("unknown should be usable: %s", got)
	}
	if got := ChooseWorker(candidates, nil, nil, []string{"ok", "unknown"}, now); got != "" {
		t.Fatalf("blocked chosen before reset: %s", got)
	}
	if got := ChooseWorker(candidates, nil, nil, []string{"ok", "unknown"}, reset); got != "blocked" {
		t.Fatalf("explicit reset did not release: %s", got)
	}
	candidates[0].Quota[0].ResetsAt = nil
	if got := ChooseWorker(candidates, nil, nil, []string{"ok", "unknown"}, reset); got != "" {
		t.Fatalf("invented reset: %s", got)
	}
	candidates[0].Quota = nil
	candidates[0].Slots = 0
	if got := ChooseWorker(candidates, rules, []string{"backend"}, []string{"ok"}, now); got != "unknown" {
		t.Fatalf("capacity/rule choice: %s", got)
	}
	candidates[0].Slots = 2
	if got := ChooseWorker(candidates, rules, []string{"backend"}, []string{"ok"}, now); got != "unknown" {
		t.Fatalf("situation rule ignored: %s", got)
	}
}
