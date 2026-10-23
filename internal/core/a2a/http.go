// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Store        *Store
	Config       func() (Config, error)
	Secret       func(OAuth) (secret.Secret, error)
	Transport    http.RoundTripper
	Ready        func() bool
	Wake         func()
	slots        chan struct{}
	once         sync.Once
	cursorSecret [32]byte
}
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, message}})
}
func success(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, response{JSONRPC: "2.0", ID: id, Result: result})
}
func authFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrAuthentication) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="a2a"`)
		http.Error(w, "Authentication required", 401)
	} else {
		http.Error(w, "Access denied", 403)
	}
}
func (s *Server) authenticate(ctx context.Context, c Config, p Publication, header string) (Principal, Binding, error) {
	a := Authenticator{OAuth: c.OAuth, Transport: s.Transport, Secret: func() (secret.Secret, error) { return s.Secret(c.OAuth) }}
	pr, err := a.Authenticate(ctx, header)
	if err != nil {
		return pr, Binding{}, err
	}
	b, err := p.Binding(pr, time.Now())
	return pr, b, err
}
func (s *Server) Authorized(r Record, pr Principal) (Publication, Binding, error) {
	c, err := s.Config()
	if err != nil {
		return Publication{}, Binding{}, err
	}
	if pr.Policy != digest(c.OAuth) || r.Principal.Policy != pr.Policy {
		return Publication{}, Binding{}, ErrDenied
	}
	p, err := c.Publication(r.Publication)
	if err != nil {
		return p, Binding{}, err
	}
	b, err := p.Binding(pr, time.Now())
	if err != nil {
		return p, b, err
	}
	d, err := p.Definition(b, r.Definition.ID)
	if err != nil || r.Caller != pr.Key() || r.Authority != Fingerprint(p, b, d) {
		return p, b, ErrDenied
	}
	return p, b, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("A2A-Version", "1.0")
	s.once.Do(func() {
		s.slots = make(chan struct{}, 64)
		if _, err := rand.Read(s.cursorSecret[:]); err != nil {
			panic(err)
		}
	})
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Capacity exhausted", 429)
		return
	}
	if s.Ready != nil && !s.Ready() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Core recovering", 503)
		return
	}
	c, err := s.Config()
	if err != nil {
		http.Error(w, "Publication unavailable", 503)
		return
	}
	route := strings.TrimPrefix(r.URL.Path, "/a2a/")
	id, suffix, _ := strings.Cut(route, "/")
	p, err := c.Publication(id)
	if err != nil || r.URL.RawQuery != "" || !strings.HasPrefix(r.URL.Path, "/a2a/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && suffix == ".well-known/agent-card.json" {
		writeJSON(w, card(c, p))
		return
	}
	pr, b, err := s.authenticate(r.Context(), c, p, r.Header.Get("Authorization"))
	if err != nil {
		authFailure(w, err)
		return
	}
	if r.Header.Get("A2A-Version") != "1.0" {
		failure(w, nil, -32009, "Unsupported protocol version")
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(suffix, "artifacts/") {
		s.download(w, r, c, p, pr, strings.TrimPrefix(suffix, "artifacts/"))
		return
	}
	if r.Method != http.MethodPost || suffix != "" {
		http.NotFound(w, r)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		failure(w, nil, -32005, "Unsupported media type")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxInput+1))
	if err != nil || len(raw) > MaxInput {
		http.Error(w, "Request too large", 413)
		return
	}
	var req rpcRequest
	if !json.Valid(raw) {
		failure(w, nil, -32700, "Invalid JSON")
		return
	}
	if decode(raw, &req) != nil || req.JSONRPC != "2.0" || len(req.ID) == 0 || string(req.ID) == "null" || !validRPCID(req.ID) {
		failure(w, nil, -32600, "Invalid request")
		return
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage(`{}`)
	}
	switch req.Method {
	case "SendMessage", "SendStreamingMessage":
		var send SendRequest
		if decode(req.Params, &send) != nil || send.Tenant != "" || !validHistory(send.Configuration.HistoryLength) {
			failure(w, req.ID, -32602, "Invalid parameters")
			return
		}
		if len(send.Configuration.TaskPushNotificationConfig) != 0 {
			failure(w, req.ID, -32003, "Push notifications unsupported")
			return
		}
		if len(send.Message.Extensions) > 0 {
			failure(w, req.ID, -32004, "Extensions unsupported")
			return
		}
		taskID, err := inputDefinition(send.Message)
		if err != nil {
			failure(w, req.ID, -32602, "Invalid task input")
			return
		}
		d, err := p.Definition(b, taskID)
		if err != nil {
			failure(w, req.ID, -32602, "Task is not offered")
			return
		}
		if modes := send.Configuration.AcceptedOutputModes; len(modes) > 0 && !slices.Contains(modes, d.Artifact.MediaType) {
			failure(w, req.ID, -32005, "Unsupported output media")
			return
		}
		rec, err := s.Store.Admit(r.Context(), p, b, d, pr, send, time.Now())
		if err != nil {
			s.storeFailure(w, req.ID, err)
			return
		}
		if _, _, err = s.Authorized(rec, pr); err != nil {
			s.storeFailure(w, req.ID, ErrNotFound)
			return
		}
		if s.Wake != nil {
			s.Wake()
		}
		if req.Method == "SendStreamingMessage" {
			s.observe(w, r, req.ID, rec, send.Configuration.HistoryLength, true)
			return
		}
		if !send.Configuration.ReturnImmediately {
			s.observe(w, r, req.ID, rec, send.Configuration.HistoryLength, false)
			return
		}
		success(w, req.ID, map[string]any{"task": Projection(rec.Task, send.Configuration.HistoryLength, true)})
	case "GetTask", "CancelTask", "SubscribeToTask":
		var q TaskRequest
		if decode(req.Params, &q) != nil || q.ID == "" || q.Tenant != "" || !validHistory(q.HistoryLength) {
			failure(w, req.ID, -32602, "Invalid parameters")
			return
		}
		rec, err := s.Store.Get(r.Context(), p.ID, pr.Key(), q.ID)
		if err == nil {
			_, _, err = s.Authorized(rec, pr)
		}
		if err != nil {
			s.storeFailure(w, req.ID, ErrNotFound)
			return
		}
		if req.Method == "SubscribeToTask" {
			s.observe(w, r, req.ID, rec, q.HistoryLength, true)
			return
		}
		if req.Method == "CancelTask" {
			if Terminal(rec.Task.Status.State) {
				failure(w, req.ID, -32002, "Task cannot be canceled")
				return
			}
			rec, err = s.Store.Update(r.Context(), q.ID, func(current *Record) error {
				if Terminal(current.Task.Status.State) {
					return ErrInvalid
				}
				current.CancelRequested = true
				if current.Phase == "queued" {
					SetState(current, Canceled, time.Now())
				}
				return nil
			})
			if err != nil {
				s.storeFailure(w, req.ID, err)
				return
			}
			if s.Wake != nil {
				s.Wake()
			}
		}
		if _, _, err = s.Authorized(rec, pr); err != nil {
			s.storeFailure(w, req.ID, ErrNotFound)
			return
		}
		success(w, req.ID, Projection(rec.Task, q.HistoryLength, true))
	case "ListTasks":
		s.list(w, r, req, c, p, pr)
	case "GetExtendedAgentCard":
		failure(w, req.ID, -32007, "Extended card unavailable")
	case "SetTaskPushNotificationConfig", "GetTaskPushNotificationConfig", "ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig", "CreateTaskPushNotificationConfig":
		failure(w, req.ID, -32003, "Push notifications unsupported")
	default:
		failure(w, req.ID, -32601, "Method not found")
	}
}
func validHistory(n *int) bool { return n == nil || *n >= 0 && *n <= 100 }
func inputDefinition(m Message) (string, error) {
	if m.MessageID == "" || len(m.MessageID) > 256 || m.Role != "ROLE_USER" || len(m.Parts) != 1 || len(m.ReferenceTaskIDs) > 0 {
		return "", ErrInvalid
	}
	part := m.Parts[0]
	if part.Text != nil || len(part.Raw) > 0 || part.URL != "" || len(part.Data) == 0 || part.MediaType != "" && part.MediaType != "application/json" {
		return "", ErrInvalid
	}
	var data struct {
		Task  string `json:"task"`
		Input string `json:"input"`
	}
	if decode(part.Data, &data) != nil || !identifier(data.Task) || strings.TrimSpace(data.Input) == "" || len(data.Input) > 32<<10 {
		return "", ErrInvalid
	}
	return data.Task, nil
}
func (s *Server) storeFailure(w http.ResponseWriter, id json.RawMessage, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrDenied):
		failure(w, id, -32001, "Task not found")
	case errors.Is(err, ErrInvalid):
		failure(w, id, -32602, "Invalid parameters")
	case errors.Is(err, ErrCapacity):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Capacity exhausted", 429)
	default:
		http.Error(w, "Storage unavailable", 503)
	}
}
func card(c Config, p Publication) any {
	skills := []any{}
	modes := []string{}
	for _, d := range p.Definitions {
		skills = append(skills, map[string]any{"id": d.ID, "name": d.Name, "description": d.Description, "tags": []string{"task"}, "inputModes": []string{"application/json"}, "outputModes": []string{d.Artifact.MediaType}})
		if !slices.Contains(modes, d.Artifact.MediaType) {
			modes = append(modes, d.Artifact.MediaType)
		}
	}
	return map[string]any{"name": p.Name, "description": p.Description, "version": "1", "supportedInterfaces": []any{map[string]any{"url": p.URL, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]bool{"streaming": true, "pushNotifications": false, "extendedAgentCard": false}, "securitySchemes": map[string]any{"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{"flows": map[string]any{}, "oauth2MetadataUrl": strings.TrimRight(c.OAuth.Issuer, "/") + "/.well-known/oauth-authorization-server"}}}, "securityRequirements": []any{map[string]any{"schemes": map[string]any{"oauth": map[string]any{"list": c.OAuth.Scopes}}}}, "defaultInputModes": []string{"application/json"}, "defaultOutputModes": modes, "skills": skills}
}
func (s *Server) observe(w http.ResponseWriter, r *http.Request, id json.RawMessage, rec Record, history *int, stream bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastStatus := ""
	artifacts := map[string]string{}
	started := false
	// The request context owns only the observation. Admitted work is durable.
	for {
		c, err := s.Config()
		if err != nil {
			if !started {
				http.Error(w, "Publication unavailable", 503)
			}
			return
		}
		p, err := c.Publication(rec.Publication)
		if err != nil {
			if !started {
				authFailure(w, ErrDenied)
			}
			return
		}
		pr, _, err := s.authenticate(r.Context(), c, p, r.Header.Get("Authorization"))
		if err != nil {
			if !started {
				authFailure(w, err)
			}
			return
		}
		if _, _, err = s.Authorized(rec, pr); err != nil {
			if !started {
				authFailure(w, err)
			}
			return
		}
		current, err := s.Store.Get(r.Context(), rec.Publication, pr.Key(), rec.Task.ID)
		if err != nil {
			if !started {
				s.storeFailure(w, id, err)
			}
			return
		}
		rec = current
		if stream {
			send := func(payload any) bool {
				if _, _, err := s.Authorized(rec, pr); err != nil {
					return false
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
				raw, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: payload})
				if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
					return false
				}
				return http.NewResponseController(w).Flush() == nil
			}
			if !started {
				if !send(map[string]any{"task": Projection(rec.Task, history, true)}) {
					return
				}
				started = true
				lastStatus = digest(rec.Task.Status)
				for _, artifact := range rec.Task.Artifacts {
					artifacts[artifact.ArtifactID] = digest(artifact)
				}
			} else {
				// Deliver the artifact before a terminal status closes observation.
				for _, artifact := range rec.Task.Artifacts {
					hash := digest(artifact)
					if artifacts[artifact.ArtifactID] == hash {
						continue
					}
					if !send(map[string]any{"artifactUpdate": map[string]any{"taskId": rec.Task.ID, "contextId": rec.Task.ContextID, "artifact": artifact, "lastChunk": true}}) {
						return
					}
					artifacts[artifact.ArtifactID] = hash
				}
				if hash := digest(rec.Task.Status); hash != lastStatus {
					if !send(map[string]any{"statusUpdate": map[string]any{"taskId": rec.Task.ID, "contextId": rec.Task.ContextID, "status": rec.Task.Status}}) {
						return
					}
					lastStatus = hash
				}
			}
		} else if Interrupted(rec.Task.Status.State) {
			success(w, id, map[string]any{"task": Projection(rec.Task, history, true)})
			return
		}
		if Interrupted(rec.Task.Status.State) {
			return
		}
		changed := s.Store.DB.Changed()
		select {
		case <-r.Context().Done():
			if !started {
				http.Error(w, "Observation timed out", http.StatusGatewayTimeout)
			}
			return
		case <-ticker.C:
		case <-changed:
		}
	}
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, c Config, p Publication, pr Principal, resource string) {
	task, artifact, ok := strings.Cut(resource, "/")
	if !ok || strings.Contains(artifact, "/") {
		http.NotFound(w, r)
		return
	}
	rec, err := s.Store.Get(r.Context(), p.ID, pr.Key(), task)
	if err == nil {
		_, _, err = s.Authorized(rec, pr)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, a := range rec.Task.Artifacts {
		if a.ArtifactID == artifact && len(a.Parts) == 1 {
			part := a.Parts[0]
			w.Header().Set("Content-Type", part.MediaType)
			w.Header().Set("Content-Disposition", "attachment")
			_, _ = w.Write(part.Raw)
			return
		}
	}
	http.NotFound(w, r)
}

// A cursor contains an opaque last-row identity and is authenticated against
// the caller, publication, complete query and current introspection secret.
type cursor struct {
	Query   string `json:"q"`
	Updated string `json:"u"`
	ID      string `json:"i"`
}

func (s *Server) cursorKey(Config) ([]byte, error) { return s.cursorSecret[:], nil }
func encodeCursor(v cursor, key []byte) string {
	raw, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func decodeCursor(token string, key []byte) (cursor, error) {
	var c cursor
	if len(token) > 2048 {
		return c, ErrInvalid
	}
	a, b, ok := strings.Cut(token, ".")
	if !ok {
		return c, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		return c, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return c, ErrInvalid
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) || decode(raw, &c) != nil {
		return c, ErrInvalid
	}
	return c, nil
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, req rpcRequest, c Config, p Publication, pr Principal) {
	var q ListRequest
	if decode(req.Params, &q) != nil || q.Tenant != "" || !validHistory(q.HistoryLength) || q.PageSize != nil && (*q.PageSize < 1 || *q.PageSize > 100) {
		failure(w, req.ID, -32602, "Invalid parameters")
		return
	}
	if q.Status != "" && !slices.Contains([]string{Submitted, Working, Completed, Failed, Canceled, Rejected, InputRequired, AuthRequired, Unspecified}, q.Status) {
		failure(w, req.ID, -32602, "Invalid status")
		return
	}
	if q.StatusTimestampAfter != "" {
		if _, err := time.Parse(time.RFC3339Nano, q.StatusTimestampAfter); err != nil {
			failure(w, req.ID, -32602, "Invalid timestamp")
			return
		}
	}
	token := q.PageToken
	q.PageToken = ""
	query := digest([]any{p.ID, pr.Key(), q})
	key, err := s.cursorKey(c)
	if err != nil {
		http.Error(w, "Publication unavailable", 503)
		return
	}
	var cur cursor
	if token != "" {
		cur, err = decodeCursor(token, key)
		if err != nil || cur.Query != query {
			failure(w, req.ID, -32602, "Invalid cursor")
			return
		}
	}

	size := 50
	if q.PageSize != nil {
		size = *q.PageSize
	}
	binding, err := p.Binding(pr, time.Now())
	if err != nil {
		authFailure(w, err)
		return
	}
	rows, total, err := s.Store.Page(r.Context(), p, binding, pr, q, cur, size)
	if err != nil {
		s.storeFailure(w, req.ID, err)
		return
	}
	result := ListResponse{Tasks: []Task{}, PageSize: size, TotalSize: total}
	bytes := 0
	for _, rec := range rows {
		projection := Projection(rec.Task, q.HistoryLength, q.IncludeArtifacts)
		raw, _ := json.Marshal(projection)
		if len(result.Tasks) == size || bytes+len(raw) > 1<<20 {
			if len(result.Tasks) > 0 {
				last := result.Tasks[len(result.Tasks)-1]
				result.NextPageToken = encodeCursor(cursor{query, last.Status.Timestamp, last.ID}, key)
			}
			break
		}
		result.Tasks = append(result.Tasks, projection)
		bytes += len(raw)
	}
	latest, err := s.Config()
	_, bindingErr := p.Binding(pr, time.Now())
	if err != nil || bindingErr != nil || digest(latest) != digest(c) {
		authFailure(w, ErrDenied)
		return
	}
	success(w, req.ID, result)
}

func validRPCID(raw json.RawMessage) bool {
	var id any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&id) != nil {
		return false
	}
	switch id.(type) {
	case string, json.Number:
		return true
	}
	return false
}
