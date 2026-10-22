// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

// QuotaUpdates is a coalescing hint; every wake reads the current account rows.
func (s *Session) QuotaUpdates() <-chan struct{} { return s.quotaUpdates }
func (s *Session) Quota(ctx context.Context) ([]provider.QuotaRow, error) {
	var response wire.V2GetAccountRateLimitsResponse
	if err := s.call(ctx, string(wire.ClientRequestAccountRateLimitsReadMethodAccountRateLimitsRead), wire.V2GetAccountRateLimitsParams{}, &response); err != nil {
		return nil, err
	}
	snapshots := []wire.V2RateLimitSnapshot{response.RateLimits}
	if response.RateLimitsByLimitId != nil {
		for _, snapshot := range *response.RateLimitsByLimitId {
			snapshots = append(snapshots, snapshot)
		}
	}
	var rows []provider.QuotaRow
	for _, snapshot := range snapshots {
		for _, window := range []*wire.V2RateLimitWindow{snapshot.Primary, snapshot.Secondary} {
			if window == nil {
				continue
			}
			used := float64(window.UsedPercent)
			state := provider.QuotaOK
			if used >= 100 {
				state = provider.QuotaBlocked
			}
			row := provider.QuotaRow{Provider: "codex", Window: provider.UnknownWindow, UsedPct: &used, State: state, Source: provider.Endpoint, Observed: time.Now()}
			if window.ResetsAt != nil {
				at := time.Unix(*window.ResetsAt, 0)
				row.ResetsAt = &at
			}
			rows = append(rows, row)
		}
	}
	if response.OrdinaryUsageAllowed != nil && !*response.OrdinaryUsageAllowed {
		used := 100.0
		rows = append(rows, provider.QuotaRow{Provider: "codex", Window: provider.UnknownWindow, UsedPct: &used, State: provider.QuotaBlocked, Source: provider.Endpoint, Observed: time.Now()})
	}
	return rows, nil
}
