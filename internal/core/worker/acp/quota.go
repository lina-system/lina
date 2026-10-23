// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/provider"
)

type airMetadata struct {
	Air struct {
		Version      int      `json:"version"`
		Capabilities []string `json:"capabilities"`
		Failure      *struct {
			Category string   `json:"category"`
			Severity string   `json:"severity"`
			Actions  []string `json:"actions"`
		} `json:"sessionFailure"`
	} `json:"air"`
}

func readAir(meta *map[string]json.RawMessage) (airMetadata, bool) {
	var v airMetadata
	if meta == nil {
		return v, false
	}
	err := json.Unmarshal((*meta)["jetbrains"], &v)
	return v, err == nil && v.Air.Version == 1
}
func advertisesFailures(meta *map[string]json.RawMessage) bool {
	v, ok := readAir(meta)
	return ok && slices.Contains(v.Air.Capabilities, "sessionFailure")
}

func (a *Adapter) sessionFailure(meta *map[string]json.RawMessage) {
	if !a.sessionFailures.Load() {
		return
	}
	v, ok := readAir(meta)
	if !ok || v.Air.Failure == nil {
		return
	}
	report := v.Air.Failure
	if report.Severity != "error" || report.Actions == nil {
		return
	}
	f := worker.Finish{Status: worker.Failed}
	switch {
	case report.Category == "limit" && slices.Equal(report.Actions, []string{"retry"}):
		f.Failure, f.Code = worker.RateLimitFailure, "rate_limited"
	case report.Category == "limit" && len(report.Actions) == 0:
		f.Failure, f.Code = worker.QuotaFailure, "quota_exhausted"
	case report.Category == "access" && slices.Equal(report.Actions, []string{"login"}):
		f.Failure, f.Code = worker.LoginFailure, "auth_required"
	case report.Category == "connection" && slices.Equal(report.Actions, []string{"new_session"}):
		f.Failure, f.Code = worker.ConnectionFailure, "transport_lost"
	default:
		return
	}
	a.mu.Lock()
	a.extensionFailure = &f
	a.extensionObserved = time.Now().UTC()
	a.mu.Unlock()
}

// Quota returns only reported measurements. Context-window usage is not account
// quota. An explicit limit failure without a blocked vendor bucket is unmeasured.
func (a *Adapter) Quota(ctx context.Context) ([]provider.QuotaRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rows := slices.Clone(a.quotaRows)
	blocked := false
	for i, r := range rows {
		blocked = blocked || r.State == provider.QuotaBlocked
		if r.UsedPct != nil {
			v := *r.UsedPct
			rows[i].UsedPct = &v
		}
		if r.ResetsAt != nil {
			v := *r.ResetsAt
			rows[i].ResetsAt = &v
		}
	}
	if f := a.extensionFailure; !blocked && f != nil && (f.Failure == worker.RateLimitFailure || f.Failure == worker.QuotaFailure) {
		rows = append(rows, provider.QuotaRow{Window: provider.UnknownWindow, State: provider.QuotaBlocked, Source: provider.ErrorCode, Observed: a.extensionObserved})
	}
	return rows, nil
}

func (a *Adapter) vendorQuota(meta *map[string]json.RawMessage) {
	key := a.cfg.Profile.RateLimitKey
	if key == "" || meta == nil {
		return
	}
	raw := (*meta)[key]
	var v struct {
		Status      string   `json:"status"`
		Type        string   `json:"rateLimitType"`
		Utilization *float64 `json:"utilization"`
		ResetsAt    *int64   `json:"resetsAt"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Status == "" && v.Type == "" && v.Utilization == nil && v.ResetsAt == nil {
		return
	}
	row := provider.QuotaRow{LimitID: key, Bucket: v.Type, Window: provider.UnknownWindow, State: provider.QuotaUnknown, Source: provider.ProviderEvent, Observed: time.Now().UTC()}
	switch v.Type {
	case "five_hour":
		row.Window = provider.FiveHour
	case "seven_day", "seven_day_opus", "seven_day_sonnet":
		row.Window = provider.Weekly
	}
	switch v.Status {
	case "allowed":
		row.State = provider.QuotaOK
	case "allowed_warning":
		row.State = provider.QuotaWarning
	case "rejected":
		row.State = provider.QuotaBlocked
	}
	if v.Utilization != nil && *v.Utilization >= 0 && *v.Utilization <= 1 {
		pct := *v.Utilization * 100
		row.UsedPct = &pct
	}
	if v.ResetsAt != nil && *v.ResetsAt > 0 {
		reset := time.Unix(*v.ResetsAt, 0).UTC()
		row.ResetsAt = &reset
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, prior := range a.quotaRows {
		if prior.LimitID == row.LimitID && prior.Bucket == row.Bucket {
			a.quotaRows[i] = row
			return
		}
	}
	// Bounded observations from an untrusted process. Never evict a blocked row
	// merely because the peer sent many unrelated bucket names.
	if len(a.quotaRows) < 64 {
		a.quotaRows = append(a.quotaRows, row)
	}
}
