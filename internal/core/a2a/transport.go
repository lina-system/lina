// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/kit/secret"
	"net/http"
	"strings"
	"time"
)

type rpcEnvelope struct {
	Version string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func (s *Service) authenticate(c Connection, headers http.Header) error {
	if c.OAuth != nil && !time.Now().Before(c.CredentialExpires) {
		return ErrSignIn
	}
	header, prefix, err := authentication(c)
	if err != nil {
		return err
	}
	if header == "" {
		return nil
	}
	value, err := s.Vault.Get(key(c))
	if err != nil {
		return ErrSignIn
	}
	if secret.InjectHeader(headers, header, prefix, value) != nil {
		return ErrSignIn
	}
	return nil
}
func (s *Service) request(ctx context.Context, o Origin, c Connection, p Profile, method, url string, body []byte) (*http.Response, error) {
	if origin(url) != c.Origin || s.Network.Check(ctx, url, c.PrivateHost) != nil {
		return nil, ErrDestination
	}
	if err := s.allowed(ctx, o, c, p); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	if err = s.authenticate(c, req.Header); err != nil {
		return nil, err
	}
	response, err := s.Network.client(c.Origin, c.PrivateHost, func(ctx context.Context) error { return s.allowed(ctx, o, c, p) }).Do(req)
	if err != nil {
		return nil, ErrUnknown
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		response.Body.Close()
		return nil, ErrSignIn
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, ErrUnknown
	}
	return response, nil
}
func rpcResult(raw []byte, id string) (json.RawMessage, error) {
	var e rpcEnvelope
	if json.Unmarshal(raw, &e) != nil || e.Version != "2.0" || e.ID != id || e.Error != nil || len(e.Result) == 0 {
		return nil, ErrUnknown
	}
	return e.Result, nil
}
func (s *Service) rpc(ctx context.Context, c Connection, call *Call, method string, params json.RawMessage, stream bool) error {
	id := randomID()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	response, err := s.request(ctx, call.Origin, c, call.Profile, "POST", c.Interface.URL, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if stream {
		if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			return ErrUnknown
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		var data []string
		total := 0
		consume := func() error {
			if len(data) == 0 {
				return nil
			}
			raw := []byte(strings.Join(data, "\n"))
			data = nil
			result, err := rpcResult(raw, id)
			if err != nil {
				return err
			}
			return s.streamResult(ctx, c, call, result)
		}
		for scanner.Scan() {
			line := scanner.Text()
			total += len(line)
			if total > 4<<20 {
				return ErrInvalid
			}
			if line == "" {
				if err = consume(); err != nil {
					return err
				}
				if call.State != "working" && call.State != "unknown" {
					return nil
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if scanner.Err() != nil {
			return ErrUnknown
		}
		if err = consume(); err != nil {
			return err
		}
		return ErrUnknown // EOF alone never proves completion.
	}
	raw, err := readBounded(response.Body, 2<<20)
	if err != nil {
		return err
	}
	result, err := rpcResult(raw, id)
	if err != nil {
		return err
	}
	if method == "SendMessage" {
		return s.streamResult(ctx, c, call, result)
	}
	var task Task
	if json.Unmarshal(result, &task) != nil {
		return ErrUnknown
	}
	return s.observeTask(ctx, c, call, task)
}
func (s *Service) streamResult(ctx context.Context, c Connection, call *Call, raw []byte) error {
	var response streamResponse
	if json.Unmarshal(raw, &response) != nil {
		return ErrUnknown
	}
	count := 0
	for _, yes := range []bool{response.Task != nil, response.Message != nil, response.Status != nil, response.Artifact != nil} {
		if yes {
			count++
		}
	}
	if count != 1 {
		return ErrUnknown
	}
	switch {
	case response.Task != nil:
		return s.observeTask(ctx, c, call, *response.Task)
	case response.Message != nil:
		m := response.Message
		if s.reflectsCredential(c, []byte(m.ID)) || s.reflectsCredential(c, []byte(m.Context)) || m.ID == "" || m.Context == "" || m.Role != "ROLE_AGENT" || m.Task != "" || call.TaskID != "" {
			return ErrUnknown
		}
		call.ContextID = m.Context
		call.RemoteState = "direct_message"
		return s.verify(ctx, c, call, []Artifact{{ID: m.ID, Parts: m.Parts}})
	case response.Status != nil:
		u := response.Status
		if call.TaskID == "" || u.TaskID != call.TaskID || u.Context != call.ContextID {
			return ErrUnknown
		}
		// A full GetTask read supplies authoritative assembled artifacts after a
		// stream interruption/completion. Updates themselves are only observations.
		call.RemoteState = u.Status.State
		if u.Status.State == "TASK_STATE_INPUT_REQUIRED" || u.Status.State == "TASK_STATE_AUTH_REQUIRED" {
			return s.observeTask(ctx, c, call, Task{ID: call.TaskID, Context: call.ContextID, Status: u.Status})
		}
		return s.save(ctx, *call)
	case response.Artifact != nil:
		if response.Artifact.TaskID != call.TaskID || response.Artifact.Context != call.ContextID || call.TaskID == "" {
			return ErrUnknown
		}
		return nil
	}
	return ErrUnknown
}
