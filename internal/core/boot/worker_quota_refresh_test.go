// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type quotaRefreshProbe struct{ worker.Adapter }

func (*quotaRefreshProbe) Name() string { return "codex" }
func (*quotaRefreshProbe) Quota(context.Context) ([]provider.QuotaRow, error) {
	return []provider.QuotaRow{{Provider: provider.ChatGPT, Account: "recovered-account", Window: provider.FiveHour, State: provider.QuotaOK, Source: provider.Endpoint, Observed: time.Now()}}, nil
}

func TestRoutedWorkerQuotaRefreshesDispatch(t *testing.T) {
	root := controlRuntime(t)
	s, err := dispatch.Open(t.Context(), dispatch.Config{DB: root.db, Epoch: root.epoch})
	if err != nil {
		t.Fatal(err)
	}
	root.workerDispatch = s
	if err := s.ObserveQuota(t.Context(), "codex", []provider.QuotaRow{{Window: provider.UnknownWindow, State: provider.QuotaBlocked, Source: provider.ErrorCode, Observed: time.Now().Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	r := newConversationView(root, root.agent, root.conversation)
	r.workers.sessions["thread"] = &workerSession{adapter: &quotaRefreshProbe{}, origin: r}
	t.Cleanup(func() { delete(r.workers.sessions, "thread") })
	in, err := workerSchema("worker_quota", json.RawMessage(`{"session":"thread"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.workerOperation(t.Context(), "worker_quota", "turn", ledger.Event{}, in)
	if err != nil || result.State != tools.Applied {
		t.Fatalf("quota read: %+v %v", result, err)
	}
	var raw []byte
	if err := root.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT rows FROM worker_quota WHERE worker='codex'").Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var rows []provider.QuotaRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if chosen := work.ChooseWorker([]work.WorkerCandidate{{Name: "codex", Slots: 1, Quota: rows}}, nil, nil, nil, time.Now()); chosen != "codex" {
		t.Fatalf("successful routed quota read left dispatch blocked: %s", raw)
	}
}
