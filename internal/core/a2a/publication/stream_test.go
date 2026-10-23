// SPDX-License-Identifier: Apache-2.0
package publication

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func admittedTask(t *testing.T, s *Server) Task {
	t.Helper()
	w := rpc(t, s, "one", "1.0", "SendMessage", map[string]any{"message": map[string]any{"messageId": "once", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"task": "write", "input": "input"}}}}, "configuration": map[string]any{"returnImmediately": true}})
	var answer struct{ Result struct{ Task Task } }
	if json.Unmarshal(w.Body.Bytes(), &answer) != nil || answer.Result.Task.ID == "" {
		t.Fatal(w.Body)
	}
	return answer.Result.Task
}
func streamRequest(t *testing.T, ctx context.Context, server *httptest.Server, id, method string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]string{"id": id}})
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/a2a/published", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer one")
	req.Header.Set("A2A-Version", "1.0")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func TestStreamRevocationFencesNewArtifacts(t *testing.T) {
	s, _ := httpFixture(t)
	task := admittedTask(t, s)
	var revoked atomic.Bool
	transport := s.Transport
	s.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if revoked.Load() {
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return transport.RoundTrip(r)
	})
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp := streamRequest(t, ctx, server, task.ID, "SubscribeToTask")
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, task.ID) {
		t.Fatalf("snapshot: %s %v", line, err)
	}
	revoked.Store(true)
	if _, err = s.Store.Update(ctx, task.ID, func(r *Record) error {
		r.Task.Artifacts = []Artifact{{ArtifactID: "secret-result", Parts: []Part{{Raw: []byte("private artifact")}}}}
		SetState(r, Completed, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tail, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tail), "secret-result") || strings.Contains(string(tail), "private artifact") {
		t.Fatal("revoked stream disclosed a result")
	}
}
func TestStreamDisconnectDoesNotCancelOrRequeueWork(t *testing.T) {
	s, _ := httpFixture(t)
	task := admittedTask(t, s)
	if ok, err := s.Store.Claim(t.Context(), task.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	resp := streamRequest(t, ctx, server, task.ID, "SubscribeToTask")
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	r, err := s.Store.Read(t.Context(), task.ID)
	if err != nil || r.CancelRequested || r.Phase != "executing" {
		t.Fatalf("stream changed execution: %+v %v", r, err)
	}
	if ok, err := s.Store.Claim(t.Context(), task.ID); err != nil || ok {
		t.Fatal("stream requeued work", err)
	}
}
func TestCancellationAcknowledgmentAndDownloadsStayScoped(t *testing.T) {
	s, _ := httpFixture(t)
	task := admittedTask(t, s)
	if _, err := s.Store.Update(t.Context(), task.ID, func(r *Record) error { r.Phase = "unknown"; SetState(r, Unspecified, time.Now()); return nil }); err != nil {
		t.Fatal(err)
	}
	w := rpc(t, s, "one", "1.0", "CancelTask", map[string]string{"id": task.ID})
	if strings.Contains(w.Body.String(), Canceled) || !strings.Contains(w.Body.String(), Unspecified) {
		t.Fatalf("acknowledgment asserted stopped execution: %s", w.Body)
	}
	if _, err := s.Store.Update(t.Context(), task.ID, func(r *Record) error {
		r.Task.Artifacts = []Artifact{{ArtifactID: "result", Parts: []Part{{Raw: []byte("permitted result"), MediaType: "text/plain"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"one", "two"} {
		request := httptest.NewRequest("GET", "https://core.example/a2a/published/artifacts/"+task.ID+"/result", nil)
		request.Header.Set("Authorization", "Bearer "+caller)
		request.Header.Set("A2A-Version", "1.0")
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		if caller == "one" {
			if response.Code != 200 || response.Body.String() != "permitted result" {
				t.Fatal(response.Code, response.Body)
			}
		} else if response.Code != 404 || strings.Contains(response.Body.String(), "permitted result") {
			t.Fatal("cross-caller download", response.Code, response.Body)
		}
	}
}

func TestStreamUsesProtoStatusAndArtifactUpdates(t *testing.T) {
	s, _ := httpFixture(t)
	task := admittedTask(t, s)
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp := streamRequest(t, ctx, server, task.ID, "SubscribeToTask")
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(first, `"task":`) {
		t.Fatalf("missing initial snapshot: %s %v", first, err)
	}
	if _, err = s.Store.Update(ctx, task.ID, func(r *Record) error {
		r.Task.Artifacts = []Artifact{{ArtifactID: "result", Parts: []Part{{Raw: []byte("result"), MediaType: "text/plain"}}}}
		SetState(r, Completed, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), `"artifactUpdate":`) || !strings.Contains(string(rest), `"statusUpdate":`) || !strings.Contains(string(rest), `"lastChunk":true`) {
		t.Fatalf("missing proto update events: %s", rest)
	}
	if strings.Index(string(rest), `"artifactUpdate":`) > strings.Index(string(rest), `"statusUpdate":`) {
		t.Fatal("terminal status preceded its artifact")
	}
}
