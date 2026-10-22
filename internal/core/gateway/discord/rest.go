// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"math"
	"net/http"
	"time"
)

// No automatic write retry. In particular, network errors and 5xx replies may
// follow an applied effect. Return only typed, token-free outcomes to Core.
func (a *Adapter) request(ctx context.Context, method, path string, body, out any) (gateway.SendResult, error) {
	unknown := gateway.SendResult{Outcome: gateway.Uncertain}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return unknown, errResponse
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://discord.com/api/v10"+path, bytes.NewReader(data))
	if err != nil {
		return unknown, errUnavailable
	}
	if err = secret.InjectHeader(req.Header, "Authorization", "Bot ", a.token); err != nil {
		return unknown, errUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LINA (https://github.com/thisisjun786/lina)")
	resp, err := a.http.Do(req)
	if err != nil {
		return unknown, errUnavailable
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return unknown, errResponse
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && json.Unmarshal(data, out) != nil {
			return unknown, errResponse
		}
		return gateway.SendResult{Outcome: gateway.Delivered}, nil
	}
	var f struct {
		Code  int     `json:"code"`
		Retry float64 `json:"retry_after"`
	}
	_ = json.Unmarshal(data, &f)
	switch {
	case resp.StatusCode == 429:
		if f.Retry <= 0 || math.IsNaN(f.Retry) || math.IsInf(f.Retry, 0) || f.Retry > float64(math.MaxInt64)/float64(time.Second) {
			return unknown, nil
		}
		return gateway.SendResult{Outcome: gateway.RetryLater, RetryAfter: time.Duration(math.Ceil(f.Retry * float64(time.Second)))}, nil
	case resp.StatusCode == 401:
		return gateway.SendResult{Outcome: gateway.Unauthorized}, nil
	case resp.StatusCode == 403 || resp.StatusCode == 404 && f.Code == 10003:
		return gateway.SendResult{Outcome: gateway.PermanentlyBlocked}, nil
	case resp.StatusCode == 400 && f.Code == 50035:
		return gateway.SendResult{Outcome: gateway.TooLong}, nil
	default:
		return unknown, nil
	}
}
