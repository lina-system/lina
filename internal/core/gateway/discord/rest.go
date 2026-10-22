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
	"mime/multipart"
	"net/http"
	"time"
)

type fileMessage struct{ Text string }

// No automatic write retry. In particular, network errors and 5xx replies may
// follow an applied effect. Return only typed, token-free outcomes to Core.
func (a *Adapter) request(ctx context.Context, method, path string, body, out any) (gateway.SendResult, error) {
	unknown := gateway.SendResult{Outcome: gateway.Uncertain}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var data []byte
	var err error
	contentType := "application/json"
	if file, ok := body.(fileMessage); ok {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		meta, _ := json.Marshal(map[string]any{"content": "Full reply attached.", "allowed_mentions": map[string]any{"parse": []string{}}, "attachments": []any{map[string]any{"id": 0, "filename": "reply.txt"}}})
		if err = writer.WriteField("payload_json", string(meta)); err != nil {
			return unknown, errResponse
		}
		part, e := writer.CreateFormFile("files[0]", "reply.txt")
		if e != nil {
			return unknown, errResponse
		}
		if _, err = io.WriteString(part, file.Text); err != nil {
			return unknown, errResponse
		}
		if err = writer.Close(); err != nil {
			return unknown, errResponse
		}
		data = buffer.Bytes()
		contentType = writer.FormDataContentType()
	} else if body != nil {
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
	req.Header.Set("Content-Type", contentType)
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
