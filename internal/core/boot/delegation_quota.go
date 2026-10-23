// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/provider"
)

// inferredQuotaStop is a quota or rate-limit finish that the quota snapshot
// did not already show as exhausted. It blocks the worker until reset or a
// later measurement of the same limit.
type inferredQuotaStop struct {
	at       time.Time
	reset    time.Time
	baseline time.Time
	armed    bool
}

func (d *delegationRuntime) noteQuotaFailure(now time.Time) {
	var snapshots [][]provider.QuotaRow
	if d.adapter != nil {
		if rows := d.adapter.CachedQuota(); len(rows) > 0 {
			snapshots = append(snapshots, rows)
		}
	}
	d.mu.Lock()
	sessions := make([]*codex.Session, 0, len(d.adapters))
	for _, session := range d.adapters {
		sessions = append(sessions, session)
	}
	d.mu.Unlock()
	for _, session := range sessions {
		if rows := session.CachedQuota(); len(rows) > 0 {
			snapshots = append(snapshots, rows)
		}
	}
	stop := &inferredQuotaStop{at: now, baseline: now}
	for _, row := range mergeQuotaWindows(snapshots...) {
		adoptQuotaReset(stop, row, now)
	}
	d.mu.Lock()
	d.quotaStop = stop
	d.mu.Unlock()
}

// reconcileQuotaStop merges per-window observations with an error-inferred stop.
// The first read after the stop is absorbed so the snapshot that missed the
// failure cannot clear it. A later measurement under the cap can.
func (d *delegationRuntime) reconcileQuotaStop(rows []provider.QuotaRow, now time.Time) (bool, time.Time) {
	rowBlocked, rowReset := quotaRowsBlocked(rows, now)
	d.mu.Lock()
	defer d.mu.Unlock()
	stop := d.quotaStop
	if stop == nil {
		return rowBlocked, rowReset
	}
	if !stop.reset.IsZero() && !now.Before(stop.reset) && !rowBlocked {
		d.quotaStop = nil
		return false, time.Time{}
	}
	if !stop.armed {
		stop.armed = true
		for _, row := range rows {
			if row.Observed.After(stop.baseline) {
				stop.baseline = row.Observed
			}
			adoptQuotaReset(stop, row, now)
		}
		return true, earlierReset(stop.reset, rowReset)
	}
	for _, row := range rows {
		adoptQuotaReset(stop, row, now)
	}
	recovered := false
	for _, row := range rows {
		if !row.Observed.After(stop.baseline) || row.UsedPct == nil || *row.UsedPct >= 100 || row.State == provider.QuotaBlocked {
			continue
		}
		recovered = true
		break
	}
	if (recovered || !stop.reset.IsZero() && !now.Before(stop.reset)) && !rowBlocked {
		d.quotaStop = nil
		return false, rowReset
	}
	return true, earlierReset(stop.reset, rowReset)
}

func adoptQuotaReset(stop *inferredQuotaStop, row provider.QuotaRow, now time.Time) {
	if !quotaRowExhausted(row, now) || row.ResetsAt == nil || !row.ResetsAt.After(now) {
		return
	}
	if stop.reset.IsZero() || row.ResetsAt.Before(stop.reset) {
		stop.reset = *row.ResetsAt
	}
}

func quotaRowExhausted(row provider.QuotaRow, now time.Time) bool {
	return row.UsedPct != nil && *row.UsedPct >= 100 && (row.ResetsAt == nil || row.ResetsAt.After(now))
}

func quotaRowsBlocked(rows []provider.QuotaRow, now time.Time) (bool, time.Time) {
	blocked := false
	var reset time.Time
	for _, row := range rows {
		if !quotaRowExhausted(row, now) {
			continue
		}
		blocked = true
		if row.ResetsAt != nil && (reset.IsZero() || row.ResetsAt.Before(reset)) {
			reset = *row.ResetsAt
		}
	}
	return blocked, reset
}

func earlierReset(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() || a.Before(b) {
		return a
	}
	return b
}

// mergeQuotaWindows keeps the newest observation of each bucket. A newer row
// in one window does not replace an older row in another.
func mergeQuotaWindows(snapshots ...[]provider.QuotaRow) []provider.QuotaRow {
	best := map[string]provider.QuotaRow{}
	var order []string
	for _, rows := range snapshots {
		for _, row := range rows {
			key := quotaWindowKey(row)
			prev, ok := best[key]
			if !ok {
				order = append(order, key)
				best[key] = row
				continue
			}
			if row.Observed.After(prev.Observed) {
				best[key] = row
			}
		}
	}
	merged := make([]provider.QuotaRow, 0, len(order))
	for _, key := range order {
		merged = append(merged, best[key])
	}
	return merged
}

func quotaWindowKey(row provider.QuotaRow) string {
	window := ""
	if row.Bucket == "" {
		window = string(row.Window)
	}
	return strings.Join([]string{string(row.Provider), string(row.Account), row.LimitID, row.Bucket, window}, "\x00")
}
