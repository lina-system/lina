// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

type quotaUpdate struct {
	raw      json.RawMessage
	observed time.Time
}
type quotaLimit struct {
	fields   map[string]json.RawMessage
	source   map[string]provider.QuotaSource
	observed map[string]time.Time
}

// Quota reads the account snapshot once; the ordered notification stream then
// maintains it for this process lifetime. It never reads Core credentials.
func (s *Session) Quota(ctx context.Context) ([]provider.QuotaRow, error) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.failure != nil || s.closing {
		err := s.failure
		if err == nil {
			err = errors.New("Codex session closed")
		}
		s.mu.Unlock()
		return nil, err
	}
	loaded := s.quotaLoaded
	s.mu.Unlock()
	if !loaded {
		var raw json.RawMessage
		err := s.callGuarded(ctx, string(wire.ClientRequestAccountRateLimitsReadMethodAccountRateLimitsRead), wire.V2GetAccountRateLimitsParams{}, &raw, func() error {
			// The endpoint supersedes all observations before this dispatch. Only
			// notifications received while the read is in flight may override it.
			s.quotaReading = true
			s.quotaUpdates = nil
			return nil
		})
		if err != nil {
			s.mu.Lock()
			s.quotaReading = false
			s.quotaUpdates = nil
			s.mu.Unlock()
			return nil, err
		}
		s.mu.Lock()
		err = s.loadQuota(raw)
		s.quotaReading = false
		s.quotaUpdates = nil
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaRows(), nil
}

func (s *Session) loadQuota(raw json.RawMessage) error {
	var v wire.V2GetAccountRateLimitsResponse
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields["rateLimits"]) == 0 || bytes.Equal(fields["rateLimits"], []byte("null")) {
		return errors.New("Codex quota reply omitted rateLimits")
	}
	if v.AccountId != nil {
		s.quotaAccount = *v.AccountId
	}
	if v.RateLimits.LimitId != nil {
		s.quotaDefault = *v.RateLimits.LimitId
	}
	s.quotaLimits = map[string]*quotaLimit{}
	now := time.Now().UTC()
	if err := s.mergeQuota(s.quotaDefault, fields["rateLimits"], provider.Endpoint, now); err != nil {
		return err
	}
	var byID map[string]json.RawMessage
	if field := fields["rateLimitsByLimitId"]; len(field) > 0 {
		if err := json.Unmarshal(field, &byID); err != nil {
			return err
		}
	}
	for id, bucket := range byID {
		if err := s.mergeQuota(id, bucket, provider.Endpoint, now); err != nil {
			return err
		}
	}
	for _, update := range s.quotaUpdates {
		if err := s.applyQuotaUpdate(update); err != nil {
			return err
		}
	}
	s.quotaUpdates = nil
	s.quotaLoaded = true
	return nil
}

func (s *Session) quotaUpdated(raw []byte) error {
	// Generated types classify the notification; retain presence separately because
	// the generated required int64 usedPercent cannot distinguish missing from zero.
	var envelope struct {
		Params struct {
			RateLimits json.RawMessage `json:"rateLimits"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	update := quotaUpdate{raw: append(json.RawMessage(nil), envelope.Params.RateLimits...), observed: time.Now().UTC()}
	if !s.quotaLoaded {
		if !s.quotaReading {
			return nil
		}
		if len(s.quotaUpdates) >= 256 {
			return errors.New("Codex quota updates need a snapshot")
		}
		s.quotaUpdates = append(s.quotaUpdates, update)
		return nil
	}
	return s.applyQuotaUpdate(update)
}
func (s *Session) applyQuotaUpdate(update quotaUpdate) error {
	var v wire.V2RateLimitSnapshot
	if err := json.Unmarshal(update.raw, &v); err != nil {
		return err
	}
	id := s.quotaDefault
	if v.LimitId != nil {
		id = *v.LimitId
	}
	return s.mergeQuota(id, update.raw, provider.ProviderEvent, update.observed)
}

func (s *Session) mergeQuota(id string, raw json.RawMessage, source provider.QuotaSource, observed time.Time) error {
	var incoming map[string]json.RawMessage
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return err
	}
	if incoming == nil {
		return errors.New("invalid Codex quota snapshot")
	}
	limit := s.quotaLimits[id]
	if limit == nil {
		limit = &quotaLimit{fields: map[string]json.RawMessage{}, source: map[string]provider.QuotaSource{}, observed: map[string]time.Time{}}
		s.quotaLimits[id] = limit
	}
	for key, value := range incoming {
		if bytes.Equal(value, []byte("null")) {
			continue
		}
		switch key {
		case "primary", "secondary", "individualLimit", "credits":
			var patch map[string]json.RawMessage
			if err := json.Unmarshal(value, &patch); err != nil {
				return err
			}
			merged := map[string]json.RawMessage{}
			if previous := limit.fields[key]; len(previous) > 0 {
				if err := json.Unmarshal(previous, &merged); err != nil {
					return err
				}
			}
			for field, v := range patch {
				if bytes.Equal(v, []byte("null")) {
					// Account metadata is sparse, but an explicitly null measurement is
					// unknown, not the previous measurement and never a numeric zero.
					if field == "usedPercent" || field == "remainingPercent" {
						delete(merged, field)
					}
					continue
				}
				merged[field] = v
			}
			value, _ = json.Marshal(merged)
		}
		limit.fields[key] = append(json.RawMessage(nil), value...)
		limit.source[key] = source
		limit.observed[key] = observed
	}
	limit.source[""] = source
	limit.observed[""] = observed
	return nil
}

func (s *Session) quotaRows() []provider.QuotaRow {
	ids := make([]string, 0, len(s.quotaLimits))
	for id := range s.quotaLimits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rows []provider.QuotaRow
	for _, id := range ids {
		limit := s.quotaLimits[id]
		start := len(rows)
		for _, key := range []string{"primary", "secondary"} {
			raw := limit.fields[key]
			if len(raw) == 0 {
				continue
			}
			row := s.quotaRow(id, limit, key)
			var v wire.V2RateLimitWindow
			if json.Unmarshal(raw, &v) == nil {
				var fields map[string]json.RawMessage
				json.Unmarshal(raw, &fields)
				if used := fields["usedPercent"]; len(used) > 0 && !bytes.Equal(used, []byte("null")) && v.UsedPercent >= 0 {
					value := float64(v.UsedPercent)
					row.UsedPct = &value
					row.State = quotaState(value)
				}
				if v.WindowDurationMins != nil {
					switch *v.WindowDurationMins {
					case 300:
						row.Window = provider.FiveHour
					case 10080:
						row.Window = provider.Weekly
					case 1440:
						row.Window = provider.Daily
					case 1:
						row.Window = provider.PerMinute
					}
				}
				if v.ResetsAt != nil {
					reset := time.Unix(*v.ResetsAt, 0).UTC()
					row.ResetsAt = &reset
				}
			}
			rows = append(rows, row)
		}
		if raw := limit.fields["individualLimit"]; len(raw) > 0 {
			row := s.quotaRow(id, limit, "individualLimit")
			row.Window = provider.SpendCap
			var v wire.V2SpendControlLimitSnapshot
			if json.Unmarshal(raw, &v) == nil {
				var fields map[string]json.RawMessage
				json.Unmarshal(raw, &fields)
				if _, ok := fields["remainingPercent"]; ok && v.RemainingPercent >= 0 && v.RemainingPercent <= 100 {
					used := float64(100 - v.RemainingPercent)
					row.UsedPct = &used
					row.State = quotaState(used)
				}
				if _, ok := fields["resetsAt"]; ok {
					reset := time.Unix(v.ResetsAt, 0).UTC()
					row.ResetsAt = &reset
				}
			}
			rows = append(rows, row)
		}
		if len(rows) == start {
			rows = append(rows, s.quotaRow(id, limit, ""))
		}
	}
	return rows
}
func (s *Session) quotaRow(id string, limit *quotaLimit, key string) provider.QuotaRow {
	return provider.QuotaRow{Provider: provider.ChatGPT, Account: provider.AccountID(s.quotaAccount), LimitID: id, Bucket: key, Window: provider.UnknownWindow, State: provider.QuotaUnknown, Source: limit.source[key], Observed: limit.observed[key]}
}
func quotaState(used float64) provider.QuotaState {
	if used >= 100 {
		return provider.QuotaBlocked
	}
	return provider.QuotaOK
}
