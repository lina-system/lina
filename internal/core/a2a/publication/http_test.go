// SPDX-License-Identifier: Apache-2.0
package publication

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func httpFixture(t *testing.T) (*Server, *Config) {
	now := time.Now()
	cfg := &Config{Enabled: true, OAuth: OAuth{Issuer: "https://id.example", Audience: "a2a", IntrospectionURL: "https://id.example/introspect", ClientID: "core", Scopes: []string{"tasks"}}, Publications: []Publication{{ID: "published", Agent: "agent", Conversation: "main:agent", Name: "Published", Description: "Approved tasks", URL: "https://core.example/a2a/published", Definitions: []Definition{{ID: "write", Name: "Write", Description: "Produce result", Seconds: 60, Artifact: ArtifactRule{MediaType: "text/plain"}}}, Bindings: []Binding{{Subject: "one", Client: "client", Definitions: []string{"write"}, Expires: now.Add(time.Hour).Unix(), MaxActive: 2}, {Subject: "two", Client: "client", Definitions: []string{"write"}, Expires: now.Add(time.Hour).Unix(), MaxActive: 2}}}}}
	s := &Server{Store: testStore(t), Config: func() (Config, error) { return *cfg, nil }, Secret: func(OAuth) (secret.Secret, error) {
		return secret.New(base64.StdEncoding.EncodeToString([]byte("core:fixture-secret"))), nil
	}, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.ParseForm()
		subject := r.Form.Get("token")
		body := fmt.Sprintf(`{"active":true,"iss":"https://id.example","aud":"a2a","exp":%d,"sub":%q,"client_id":"client","scope":"tasks","token_type":"Bearer"}`, now.Add(time.Hour).Unix(), subject)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return s, cfg
}
func rpc(t *testing.T, s *Server, caller, version, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	req := httptest.NewRequest("POST", "https://core.example/a2a/published", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+caller)
	req.Header.Set("A2A-Version", version)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}
func TestHTTPAdmissionAndCallerIsolation(t *testing.T) {
	s, cfg := httpFixture(t)
	params := map[string]any{"message": map[string]any{"messageId": "once", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"task": "write", "input": "result"}}}}, "configuration": map[string]any{"returnImmediately": true}}
	w := rpc(t, s, "one", "1.0", "SendMessage", params)
	if w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	var answer struct{ Result struct{ Task Task } }
	if json.Unmarshal(w.Body.Bytes(), &answer) != nil || answer.Result.Task.ID == "" {
		t.Fatal(w.Body)
	}
	id := answer.Result.Task.ID
	w = rpc(t, s, "one", "1.0", "SendMessage", params)
	var retry struct{ Result struct{ Task Task } }
	json.Unmarshal(w.Body.Bytes(), &retry)
	if retry.Result.Task.ID != id {
		t.Fatal("duplicate admission")
	}
	for _, method := range []string{"GetTask", "SubscribeToTask", "CancelTask"} {
		w = rpc(t, s, "two", "1.0", method, map[string]any{"id": id})
		if !strings.Contains(w.Body.String(), `"code":-32001`) {
			t.Fatalf("%s cross caller: %s", method, w.Body)
		}
	}
	w = rpc(t, s, "two", "1.0", "ListTasks", map[string]any{})
	if strings.Contains(w.Body.String(), id) || !strings.Contains(w.Body.String(), `"totalSize":0`) {
		t.Fatalf("list leaked: %s", w.Body)
	}
	w = rpc(t, s, "one", "", "GetTask", map[string]any{"id": id})
	if !strings.Contains(w.Body.String(), `"code":-32009`) {
		t.Fatalf("version accepted: %s", w.Body)
	}
	cfg.Publications[0].Bindings[0].Expires = 1
	w = rpc(t, s, "one", "1.0", "GetTask", map[string]any{"id": id})
	if w.Code != 403 {
		t.Fatalf("revoked: %d %s", w.Code, w.Body)
	}
}
func TestAgentCardIsApprovedMetadataOnly(t *testing.T) {
	s, _ := httpFixture(t)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "https://core.example/a2a/published/.well-known/agent-card.json", nil))
	var card map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &card) != nil {
		t.Fatal(w.Body)
	}
	if card["supportedInterfaces"] == nil || card["securityRequirements"] == nil || strings.Contains(w.Body.String(), "main:agent") || strings.Contains(w.Body.String(), "fixture-secret") {
		t.Fatalf("unsafe card: %s", w.Body)
	}
}

func TestRetryCannotDiscloseAfterPublicationScopeChanges(t *testing.T) {
	s, cfg := httpFixture(t)
	params := map[string]any{"message": map[string]any{"messageId": "once", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"task": "write", "input": "result"}}}}, "configuration": map[string]any{"returnImmediately": true}}
	w := rpc(t, s, "one", "1.0", "SendMessage", params)
	var out struct{ Result struct{ Task Task } }
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Result.Task.ID == "" {
		t.Fatal(w.Body)
	}
	if _, err := s.Store.Update(t.Context(), out.Result.Task.ID, func(r *Record) error {
		r.Task.Artifacts = []Artifact{{ArtifactID: "private-result", Parts: []Part{{Raw: []byte("previous scope")}}}}
		SetState(r, Completed, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg.Publications[0].Definitions[0].Artifact.MediaType = "application/json"
	w = rpc(t, s, "one", "1.0", "SendMessage", params)
	if strings.Contains(w.Body.String(), "private-result") || !strings.Contains(w.Body.String(), `"code":-32001`) {
		t.Fatalf("retry disclosed a superseded scope: %s", w.Body)
	}
}
