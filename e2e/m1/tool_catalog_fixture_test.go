// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Legacy scripted model cases select an operation in one step. Teach that
// fixture the real discovery protocol: load a deferred tool, observe its
// declaration, then emit the saved operation. Core still executes and journals
// both calls. M4 tests inspect the unmodified first request separately.
func toolCatalogModel(next http.Handler) http.Handler {
	type pendingReply struct {
		body   []byte
		header http.Header
		code   int
		names  []string
	}
	var mu sync.Mutex
	var sequence atomic.Uint64
	pending := map[string]pendingReply{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var req struct {
			Instructions string
			Tools        []struct {
				Name       string
				Parameters struct {
					Properties struct {
						Tools struct{ Items struct{ Enum []string } }
					}
				}
			}
		}
		_ = json.Unmarshal(raw, &req)
		var available []string
		offered := map[string]bool{}
		for _, d := range req.Tools {
			offered[d.Name] = true
			if d.Name == "load_tools" {
				available = d.Parameters.Properties.Tools.Items.Enum
			}
		}
		if available == nil {
			next.ServeHTTP(w, r)
			return
		}
		var persona struct {
			AgentID string `json:"agent_id"`
		}
		_ = json.Unmarshal(instructionBlock(req.Instructions, "persona"), &persona)
		mu.Lock()
		saved, exists := pending[persona.AgentID]
		if exists {
			delete(pending, persona.AgentID)
		}
		mu.Unlock()
		if exists {
			for _, name := range saved.names {
				if !offered[name] {
					http.Error(w, "fixture tool loading failed", 500)
					return
				}
			}
		} else {
			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, r)
			saved = pendingReply{body: recorder.Body.Bytes(), header: recorder.Header(), code: recorder.Code}
			for _, line := range strings.Split(string(saved.body), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Type string
					Item struct{ Type, Name string }
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil || event.Type != "response.output_item.done" || event.Item.Type != "function_call" {
					continue
				}
				name := event.Item.Name
				if !offered[name] && slices.Contains(available, name) && !slices.Contains(saved.names, name) {
					saved.names = append(saved.names, name)
				}
			}
			if len(saved.names) > 0 {
				mu.Lock()
				pending[persona.AgentID] = saved
				mu.Unlock()
				emitPersonaCall(w, fmt.Sprintf("fixture-load-%d", sequence.Add(1)), "load_tools", map[string]any{"tools": saved.names})
				return
			}
		}
		for key, values := range saved.header {
			w.Header()[key] = values
		}
		w.WriteHeader(saved.code)
		_, _ = w.Write(saved.body)
	})
}
