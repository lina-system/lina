// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/provider"
)

// inferredQuotaStop is a quota or rate-limit finish that the quota snapshot
// did not already show as exhausted. It blocks the worker until every stopped
// limit reaches the reset that limit reported, or a later measurement shows
// that same limit below the cap. limits is nil until a snapshot identifies
// them; the map value is that limit's reported reset, or zero when it has none.
type inferredQuotaStop struct {
	baseline time.Time
	armed    bool
	limits   map[string]time.Time
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
	stop := &inferredQuotaStop{baseline: now, limits: stoppedLimitResets(mergeQuotaWindows(snapshots...), now)}
	d.mu.Lock()
	d.quotaStop = stop
	d.mu.Unlock()
}

// reconcileQuotaStop merges per-window observations with an error-inferred stop.
// The first read after the stop is absorbed so the snapshot that missed the
// failure cannot clear it. A later measurement clears one stopped limit only
// when that same limit is below the cap.
func (d *delegationRuntime) reconcileQuotaStop(rows []provider.QuotaRow, now time.Time) (bool, time.Time) {
	rowBlocked, rowReset := quotaRowsBlocked(rows, now)
	d.mu.Lock()
	defer d.mu.Unlock()
	stop := d.quotaStop
	if stop == nil {
		return rowBlocked, rowReset
	}
	allowRecovery := stop.armed
	if !stop.armed {
		stop.armed = true
		for _, row := range rows {
			if row.Observed.After(stop.baseline) {
				stop.baseline = row.Observed
			}
		}
		if stop.limits == nil {
			stop.limits = stoppedLimitResets(rows, now)
		}
	}
	stop.adoptNewerResets(rows, now)
	if len(stop.limits) == 0 {
		recovered := false
		for _, row := range rows {
			if allowRecovery && row.Observed.After(stop.baseline) && quotaRowUnderCap(row) {
				recovered = true
				break
			}
		}
		if recovered && !rowBlocked {
			d.quotaStop = nil
			return false, rowReset
		}
		return true, rowReset
	}
	blocking, next := stop.hold(rows, now, allowRecovery)
	if len(stop.limits) == 0 {
		d.quotaStop = nil
		return rowBlocked, rowReset
	}
	if !blocking && !rowBlocked {
		d.quotaStop = nil
		return false, rowReset
	}
	return true, earlierReset(next, rowReset)
}

// stoppedLimitResets chooses the limits a quota failure parks. Measured
// exhausted windows win. Otherwise every cached row tied for the highest
// reported usage is parked, and a snapshot with no usage parks every row it
// reported. A future reset is kept even when usage is still under the cap.
func stoppedLimitResets(rows []provider.QuotaRow, now time.Time) map[string]time.Time {
	chosen := make([]provider.QuotaRow, 0)
	for _, row := range rows {
		if rowMeasuredStop(row, now) {
			chosen = append(chosen, row)
		}
	}
	if len(chosen) == 0 {
		chosen = highestUsageRows(rows)
	}
	if len(chosen) == 0 {
		return nil
	}
	limits := make(map[string]time.Time, len(chosen))
	for _, row := range chosen {
		key := quotaWindowKey(row)
		reset := limits[key]
		if row.ResetsAt != nil && row.ResetsAt.After(now) && (reset.IsZero() || row.ResetsAt.Before(reset)) {
			reset = *row.ResetsAt
		}
		limits[key] = reset
	}
	return limits
}

func highestUsageRows(rows []provider.QuotaRow) []provider.QuotaRow {
	var max float64
	have := false
	for _, row := range rows {
		if row.UsedPct == nil {
			continue
		}
		if !have || *row.UsedPct > max {
			max = *row.UsedPct
			have = true
		}
	}
	if !have {
		if len(rows) == 0 {
			return nil
		}
		return rows
	}
	chosen := make([]provider.QuotaRow, 0, len(rows))
	for _, row := range rows {
		if row.UsedPct != nil && *row.UsedPct == max {
			chosen = append(chosen, row)
		}
	}
	return chosen
}

func rowMeasuredStop(row provider.QuotaRow, now time.Time) bool {
	if quotaRowExhausted(row, now) {
		return true
	}
	if row.State != provider.QuotaBlocked {
		return false
	}
	return row.ResetsAt == nil || row.ResetsAt.After(now)
}

func (stop *inferredQuotaStop) adoptNewerResets(rows []provider.QuotaRow, now time.Time) {
	if len(stop.limits) == 0 {
		return
	}
	for _, row := range rows {
		key := quotaWindowKey(row)
		reset, ok := stop.limits[key]
		if !ok || row.ResetsAt == nil || !row.ResetsAt.After(now) {
			continue
		}
		if row.Observed.After(stop.baseline) && quotaRowUnderCap(row) {
			continue
		}
		if reset.IsZero() || row.Observed.After(stop.baseline) || row.ResetsAt.Before(reset) {
			stop.limits[key] = *row.ResetsAt
		}
	}
}

// hold reports whether any stopped limit is still parked and the earliest
// future reset among those that remain. A limit measured below the cap leaves
// the stop for good, so a later snapshot without that measurement cannot park
// it again.
func (stop *inferredQuotaStop) hold(rows []provider.QuotaRow, now time.Time, allowRecovery bool) (bool, time.Time) {
	latest := make(map[string]provider.QuotaRow, len(rows))
	for _, row := range rows {
		key := quotaWindowKey(row)
		prev, ok := latest[key]
		if !ok || row.Observed.After(prev.Observed) {
			latest[key] = row
		}
	}
	blocking := false
	var next time.Time
	for key, reset := range stop.limits {
		row, ok := latest[key]
		if allowRecovery && ok && row.Observed.After(stop.baseline) && quotaRowUnderCap(row) {
			delete(stop.limits, key)
			continue
		}
		deadline := reset
		if ok && row.ResetsAt != nil && row.ResetsAt.After(now) && !quotaRowUnderCap(row) {
			if deadline.IsZero() || row.ResetsAt.Before(deadline) {
				deadline = *row.ResetsAt
			}
		}
		if !deadline.IsZero() && !now.Before(deadline) && (!ok || !quotaRowExhausted(row, now)) {
			continue
		}
		blocking = true
		if deadline.After(now) && (next.IsZero() || deadline.Before(next)) {
			next = deadline
		}
	}
	return blocking, next
}

func quotaRowUnderCap(row provider.QuotaRow) bool {
	return row.UsedPct != nil && *row.UsedPct < 100 && row.State != provider.QuotaBlocked
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
