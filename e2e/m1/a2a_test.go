// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Scenario data lives in m3; tests share the repository's installed m1 harness.
type a2aPeer struct {
	server                *httptest.Server
	root, artifact, token string
	mu                    sync.Mutex
	sends                 map[string]int
	changed               atomic.Bool
	gets                  atomic.Int32
	oauth                 atomic.Bool
	signins               atomic.Int32
}

func newA2APeer(t *testing.T, artifact string) *a2aPeer {
	p := &a2aPeer{root: t.TempDir(), artifact: artifact, token: "synthetic-a2a-credential-801", sends: map[string]int{}}
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/authorize" {
			q := r.URL.Query()
			if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "fixture-client" {
				t.Error("OAuth PKCE/registration missing")
			}
			u, _ := url.Parse(q.Get("redirect_uri"))
			v := u.Query()
			v.Set("code", "fixture-code")
			v.Set("state", q.Get("state"))
			v.Set("iss", p.server.URL)
			u.RawQuery = v.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
		if r.URL.Path == "/token" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("code") != "fixture-code" || len(r.Form.Get("code_verifier")) < 43 || r.Form.Get("client_id") != "fixture-client" {
				t.Error("invalid OAuth exchange")
			}
			p.signins.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": p.token, "token_type": "Bearer", "expires_in": 3600, "scope": "task"})
			return
		}

		if r.Header.Get("A2A-Version") != "1.0" {
			t.Error("A2A version header missing")
		}
		if r.URL.Path == "/card" {
			name := "Controlled peer"
			if p.changed.Load() {
				name = "Changed peer"
			}
			if p.oauth.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "supportedInterfaces": []any{map[string]any{"url": p.server.URL + "/rpc", "protocolBinding": "JSONRPC", "protocolVersion": "1.0", "tenant": "owner-task"}}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain", "application/octet-stream"}, "capabilities": map[string]bool{"streaming": true}, "securitySchemes": map[string]any{"oauth": map[string]any{"oauth2SecurityScheme": map[string]any{"flows": map[string]any{"authorizationCode": map[string]any{"authorizationUrl": p.server.URL + "/authorize", "tokenUrl": p.server.URL + "/token", "scopes": map[string]string{"task": "Call tasks"}}}}}}, "securityRequirements": []any{map[string]any{"schemes": map[string]any{"oauth": map[string]any{"list": []string{"task"}}}}}})
				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "supportedInterfaces": []any{map[string]any{"url": p.server.URL + "/rpc", "protocolBinding": "JSONRPC", "protocolVersion": "1.0", "tenant": "owner-task"}}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain", "application/octet-stream"}, "capabilities": map[string]bool{"streaming": true}, "securitySchemes": map[string]any{"bearer": map[string]any{"httpAuthSecurityScheme": map[string]string{"scheme": "Bearer"}}}, "securityRequirements": []any{map[string]any{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+p.token {
			t.Error("credential missing or sent incorrectly")
		}
		if r.URL.Path == "/artifact" {
			_, _ = w.Write([]byte(p.artifact))
			return
		}
		var req struct {
			ID, Method string
			Params     struct {
				Tenant  string
				ID      string
				Message struct {
					ID      string `json:"messageId"`
					Task    string `json:"taskId"`
					Context string `json:"contextId"`
					Parts   []struct {
						Text string `json:"text"`
					}
				}
				Configuration struct {
					Immediate bool `json:"returnImmediately"`
				}
			}
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad peer request")
			return
		}
		if req.Params.Tenant != "owner-task" {
			t.Error("tenant missing")
		}
		mode := strings.TrimPrefix(req.Params.ID, "task-")
		if req.Method == "SendMessage" || req.Method == "SendStreamingMessage" {
			if len(req.Params.Message.Parts) != 1 || !req.Params.Configuration.Immediate || req.Params.Message.ID == "" {
				t.Error("bad send")
				return
			}
			mode = req.Params.Message.Parts[0].Text
			p.mu.Lock()
			p.sends[mode]++
			p.mu.Unlock()
			if err := os.WriteFile(filepath.Join(p.root, mode), []byte(p.artifact), 0600); err != nil {
				t.Error(err)
			}
			if mode == "lost" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
		}
		if req.Method == "CancelTask" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if req.Method == "GetTask" {
			p.gets.Add(1)
		}
		state := "TASK_STATE_COMPLETED"
		if mode == "input" && req.Params.Message.Task == "" {
			state = "TASK_STATE_INPUT_REQUIRED"
		}
		if mode == "auth" && p.signins.Load() < 2 {
			state = "TASK_STATE_AUTH_REQUIRED"
		}
		if mode == "stream" && req.Method == "SendStreamingMessage" {
			state = "TASK_STATE_WORKING"
		}
		part := map[string]any{"url": p.server.URL + "/artifact", "mediaType": "text/plain"}
		if mode == "binary" {
			part = map[string]any{"raw": base64.StdEncoding.EncodeToString([]byte{0xff, 0, 1, 2, 3}), "mediaType": "application/octet-stream"}
		}
		if mode == "forbidden" {
			part["url"] = "https://169.254.169.254/latest/meta-data/"
		}
		if mode == "leak" {
			part = map[string]any{"text": p.token, "mediaType": "text/plain"}
		}
		task := map[string]any{"id": "task-" + mode, "contextId": "context-" + mode, "status": map[string]any{"state": state}}
		if state == "TASK_STATE_COMPLETED" && mode != "claim" {
			task["artifacts"] = []any{map[string]any{"artifactId": "artifact-" + mode, "parts": []any{part}}}
		}
		result := any(task)
		if strings.HasPrefix(req.Method, "Send") {
			result = map[string]any{"task": task}
		}
		if mode == "direct" {
			result = map[string]any{"message": map[string]any{"messageId": "direct", "contextId": "direct-context", "role": "ROLE_AGENT", "parts": []any{map[string]any{"text": p.artifact, "mediaType": "text/plain"}}}}
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if req.Method == "SendStreamingMessage" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", raw)
			w.(http.Flusher).Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(p.server.Close)
	return p
}
func (p *a2aPeer) count(mode string) int { p.mu.Lock(); defer p.mu.Unlock(); return p.sends[mode] }
func (p *a2aPeer) config(modes []string) protocol.A2aConnection {
	digest := sha256.Sum256([]byte(p.artifact))
	host := "127.0.0.1"
	c := protocol.A2aConnection{ID: "peer", CardURL: p.server.URL + "/card", Origin: p.server.URL, PrivateHost: &host, Scheme: "bearer"}
	for _, mode := range modes {
		effect := "calculation"
		if mode == "approval" {
			effect = "external_effect"
		}
		if mode == "preset" {
			effect = "investment_order"
		}
		c.Profiles = append(c.Profiles, protocol.A2aProfile{Name: mode, Effect: effect, InputPrefix: mode, MaxInputBytes: "256", MaxOutputBytes: "4096", Sha256: hex.EncodeToString(digest[:]), MediaType: "text/plain"})
		if mode == "binary" {
			sum := sha256.Sum256([]byte{0xff, 0, 1, 2, 3})
			c.Profiles[len(c.Profiles)-1].Sha256 = hex.EncodeToString(sum[:])
			c.Profiles[len(c.Profiles)-1].MediaType = "application/octet-stream"
		}
	}
	return c
}

type a2aModel struct {
	mu     sync.Mutex
	args   map[string]any
	id     string
	asked  bool
	code   string
	status protocol.A2aCallStatus
	token  string
}

func (m *a2aModel) handler(t *testing.T) http.Handler {
	return toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var req struct{ Input, Tools []json.RawMessage }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("bad model request")
			return
		}
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), m.token) {
			t.Error("remote credential reached model")
		}
		if !toolReachable(req.Tools, "remote_agent") {
			t.Error("A2A tool not registered")
		}
		if !m.asked {
			m.asked = true
			emitPersonaCall(w, m.id, "remote_agent", m.args)
			return
		}
		for _, item := range req.Input {
			var v struct {
				Type, Output string
				CallID       string `json:"call_id"`
			}
			_ = json.Unmarshal(item, &v)
			if v.Type == "function_call_output" && v.CallID == m.id {
				var out struct {
					Code   string
					Output json.RawMessage
				}
				if json.Unmarshal([]byte(v.Output), &out) != nil {
					t.Error("bad tool output")
				}
				m.code = out.Code
				_ = json.Unmarshal(out.Output, &m.status)
			}
		}
		emitResponse(w, "Remote operation observed.", true)
	}))
}
func (m *a2aModel) set(id string, args map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.id = id
	m.args = args
	m.asked = false
	m.code = ""
	m.status = protocol.A2aCallStatus{}
}
func (m *a2aModel) result() (string, protocol.A2aCallStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.code, m.status
}
func startA2ACore(t *testing.T, p *a2aPeer, m *a2aModel, preset string) (*harness, func() *process) {
	server := httptest.NewServer(m.handler(t))
	t.Cleanup(server.Close)
	h, _ := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	cert := filepath.Join(h.root, "peer-ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "SSL_CERT_FILE="+cert, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		return core
	}
	return h, start
}
func a2aSettings(t *testing.T, h *harness, p protocol.A2aRequest) (protocol.A2aResult, *protocol.Error) {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "a2a", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.A2A(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	e, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := e.Payload.(protocol.A2aResult)
	return out, e.Error
}
func TestM3A2AInstalledCore(t *testing.T) {
	data, err := os.ReadFile("../m3/a2a.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Artifact string
		Modes    []string
	}
	if json.Unmarshal(data, &scenario) != nil {
		t.Fatal("invalid fixture")
	}
	p := newA2APeer(t, scenario.Artifact)
	m := &a2aModel{token: p.token}
	h, start := startA2ACore(t, p, m, "")
	core := start()
	cconfig := p.config(scenario.Modes)
	settings, _ := json.Marshal(protocol.A2aRequest{Operation: "configure", Connection: &cconfig, Credential: &p.token})
	configure := h.command("a2a", "--socket", h.socket)
	configure.command.Stdin = bytes.NewReader(settings)
	configure.start(t)
	configure.wait(t, 0)
	if strings.Contains(configure.stdout.String()+configure.stderr.String(), p.token) {
		t.Fatal("credential echoed by settings command")
	}
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	var lostID, inputID string
	for _, mode := range scenario.Modes {
		m.set(mode, map[string]any{"operation": "send", "connection": "peer", "task": mode, "text": mode, "stream": mode == "stream"})
		frames := admittedTurn(t, h, c, conversation, mode, "Run the configured remote task.")
		code, status := m.result()
		if code == "" {
			t.Fatalf("%s: no tool receipt", mode)
		}
		switch mode {
		case "task", "direct", "stream", "binary":
			if code != "ok" || !status.Verified || status.ResultID == nil {
				t.Fatalf("%s: %s %+v sends=%d", mode, code, status, p.count(mode))
			}
			raw, _ := json.Marshal(frames)
			if !strings.Contains(string(raw), "Remote agent result") && !strings.Contains(string(raw), scenario.Artifact) {
				t.Fatalf("%s: result not delivered to original conversation", mode)
			}
		case "lost":
			lostID = status.ID
			if status.State != "unknown" || code != "unknown" {
				t.Fatal("lost send not unknown", code, status)
			}
		case "input":
			inputID = status.ID
			if status.State != "input_required" {
				t.Fatal(status)
			}
		case "auth":
			if status.State != "auth_required" {
				t.Fatal(status)
			}
		default:
			if status.Verified {
				t.Fatal("unverified remote evidence accepted", mode)
			}
		}
		if p.count(mode) != 1 {
			t.Fatalf("%s sends=%d", mode, p.count(mode))
		}
	}
	if p.gets.Load() == 0 {
		t.Fatal("stream not recovered by polling")
	}
	// A remote input request is not authority to send another message.
	m.set("unanswered", map[string]any{"operation": "send", "connection": "peer", "task": "input", "text": "input", "call": inputID})
	admittedTurn(t, h, c, conversation, "unanswered", "Continue only if authorized.")
	if p.count("input") != 1 {
		t.Fatal("remote question authorized its own follow-up")
	}
	// The owner's exact answer admits one follow-up under the same remote identity.
	agent := strings.TrimPrefix(conversation, "main:")
	answer := "input"
	if _, err := a2aSettings(t, h, protocol.A2aRequest{Operation: "answer", CallID: &inputID, AgentID: &agent, Answer: &answer}); err != nil {
		t.Fatal(err)
	}
	m.set("answered", map[string]any{"operation": "send", "connection": "peer", "task": "input", "text": "input", "call": inputID})
	admittedTurn(t, h, c, conversation, "answered", "Use my recorded answer.")
	if p.count("input") != 2 {
		t.Fatal("recorded answer not sent")
	}
	m.set("cancel", map[string]any{"operation": "cancel", "call": inputID})
	admittedTurn(t, h, c, conversation, "cancel", "Cancel the existing remote task.")
	_, status := m.result()
	if status.State != "cancellation_unknown" {
		t.Fatal("uncertain cancellation hidden", status)
	}
	c.Close()
	h.stopCore(core)
	core = start()
	c, again := conversationClient(t, h)
	if again != conversation {
		t.Fatal("conversation changed across restart")
	}
	m.set("observe-lost", map[string]any{"operation": "status", "call": lostID})
	admittedTurn(t, h, c, conversation, "observe-lost", "Inspect the uncertain call.")
	if p.count("lost") != 1 {
		t.Fatal("restart repeated uncertain send")
	}
	historyID, err := c.History(h.ctx, protocol.HistoryGet{ConversationID: &conversation})
	if err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == historyID {
			raw, _ := json.Marshal(frame)
			if !strings.Contains(string(raw), "Remote agent result") {
				t.Fatal("restart lost results")
			}
			break
		}
	}
	c.Close()
	h.stopCore(core)
	// Retained artifacts and provenance are read from the installed canonical DB.
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		var verified, results int
		if err := tx.QueryRowContext(h.ctx, `SELECT count(*) FROM a2a_calls WHERE json_extract(record,'$.verified')=1`).Scan(&verified); err != nil {
			return err
		}
		if verified < 3 {
			t.Error("verified evidence not retained")
		}
		if err := tx.QueryRowContext(h.ctx, `SELECT count(*) FROM result_heads WHERE id LIKE 'a2a:%'`).Scan(&results); err != nil {
			return err
		}
		if results != verified {
			t.Errorf("delivery duplicated or missing: results=%d verified=%d", results, verified)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = filepath.WalkDir(h.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.Contains(path, "secrets") {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), p.token) {
			t.Errorf("credential outside vault: %s", filepath.Base(path))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestM3A2AOwnerApprovalAndFences(t *testing.T) {
	for _, scenario := range []string{"approve", "deny", "revoke-approved", "changed", "revoke", "investing", "legal"} {
		t.Run(scenario, func(t *testing.T) {
			p := newA2APeer(t, "Observable remote operation result.")
			m := &a2aModel{token: p.token}
			h, start := startA2ACore(t, p, m, "")
			core := start()
			if scenario == "investing" || scenario == "legal" {
				probe, _ := conversationClient(t, h)
				person := probe.Welcome.PersonID
				probe.Close()
				h.stopCore(core)
				seedMethodologyAgents(t, h, person, scenario)
				core = start()
			}
			config := p.config([]string{"approval", "preset", "task"})
			if _, err := a2aSettings(t, h, protocol.A2aRequest{Operation: "configure", Connection: &config, Credential: &p.token}); err != nil {
				t.Fatal(err)
			}
			c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
			topic := "waiting"
			if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Receive(h.ctx); err != nil {
				t.Fatal(err)
			}
			mode := "approval"
			switch scenario {
			case "changed":
				mode = "task"
				p.changed.Store(true)
			case "revoke":
				mode = "task"
				id := "peer"
				if _, err := a2aSettings(t, h, protocol.A2aRequest{Operation: "revoke", ConnectionID: &id}); err != nil {
					t.Fatal(err)
				}
			case "investing", "legal":
				mode = "preset"
			}
			m.set("effect", map[string]any{"operation": "send", "connection": "peer", "task": mode, "text": mode})
			key, text := "effect", "Run the configured task within my permissions."
			id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
			if err != nil {
				t.Fatal(err)
			}
			ack, done, approved := false, false, false
			for !ack || !done {
				frame, err := c.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if frame.ReplyTo != nil && *frame.ReplyTo == id {
					if frame.Error != nil {
						t.Fatal(frame.Error)
					}
					ack = true
				}
				if approval, ok := frame.Payload.(protocol.ApprovalRequest); ok && approval.Card != nil && approval.Card.Outcome == nil {
					if scenario != "approve" && scenario != "deny" && scenario != "revoke-approved" {
						t.Fatalf("forbidden request reached approval: %s", scenario)
					}
					if p.count(mode) != 0 {
						t.Fatal("remote effect preceded owner approval")
					}
					raw, _ := json.Marshal(approval)
					if !strings.Contains(string(raw), mode) {
						t.Fatal("approval lacks exact arguments")
					}
					if scenario == "revoke-approved" {
						connection := "peer"
						if _, err := a2aSettings(t, h, protocol.A2aRequest{Operation: "revoke", ConnectionID: &connection}); err != nil {
							t.Fatal(err)
						}
					}
					choice := protocol.ApprovalChoiceOnce
					if scenario == "deny" {
						choice = protocol.ApprovalChoiceDeny
					}
					if _, err = c.AnswerApproval(h.ctx, protocol.ApprovalAnswer{CardID: &approval.Card.ID, Revision: &approval.Card.Revision, Choice: choice}); err != nil {
						t.Fatal(err)
					}
					approved = true
				}
				if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
					done = true
				}
			}
			code, status := m.result()
			if scenario == "approve" {
				if !approved || p.count(mode) != 1 || code != "ok" || !status.Verified {
					t.Fatalf("approved call did not execute: %s %+v", code, status)
				}
			} else {
				if p.count(mode) != 0 || code == "ok" {
					t.Fatalf("blocked call executed: %s %s", scenario, code)
				}
			}
			c.Close()
			h.stopCore(core)
		})
	}
}

func TestM3A2ABrowserSignInWait(t *testing.T) {
	p := newA2APeer(t, "Observable remote operation result.")
	p.oauth.Store(true)
	m := &a2aModel{token: p.token}
	h, start := startA2ACore(t, p, m, "")
	core := start()
	config := p.config([]string{"auth"})
	config.Scheme = "oauth"
	config.Oauth = &protocol.A2aOauth{Issuer: p.server.URL, AuthorizationURL: p.server.URL + "/authorize", TokenURL: p.server.URL + "/token", ClientID: "fixture-client", Scopes: []string{"task"}}
	if _, err := a2aSettings(t, h, protocol.A2aRequest{Operation: "configure", Connection: &config}); err != nil {
		t.Fatal(err)
	}
	signIn := func() {
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "a2a", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		id := "peer"
		if _, err = c.A2A(h.ctx, protocol.A2aRequest{Operation: "sign_in", ConnectionID: &id}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Kind != protocol.KindSignInListen {
			t.Fatal("no browser relay", err, frame.Error)
		}
		listener, err := clientapi.ListenSignIn(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		port := json.Number(strconv.Itoa(listener.Port()))
		if _, err = c.SignInListening(h.ctx, protocol.SignInListening{Port: &port}); err != nil {
			t.Fatal(err)
		}
		frame, err = c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		open, ok := frame.Payload.(protocol.SignInOpen)
		if !ok || open.URL == nil {
			t.Fatal("no authorized browser URL", frame.Error)
		}
		response, err := p.server.Client().Get(*open.URL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		select {
		case redirect := <-listener.Redirects():
			if _, err = c.SignInRedirect(h.ctx, redirect); err != nil {
				t.Fatal(err)
			}
		case <-h.ctx.Done():
			t.Fatal("no browser callback")
		}
		frame, err = c.Receive(h.ctx)
		if err != nil || frame.Error != nil || frame.Kind != protocol.KindA2aResult {
			t.Fatal("sign-in not completed", err, frame.Error)
		}
	}
	signIn()
	c, conversation := conversationClient(t, h)
	m.set("auth-wait", map[string]any{"operation": "send", "connection": "peer", "task": "auth", "text": "auth"})
	admittedTurn(t, h, c, conversation, "auth-wait", "Call the remote agent.")
	_, status := m.result()
	if status.State != "auth_required" || status.RemoteTask == nil {
		t.Fatal("remote sign-in wait lost", status)
	}
	signIn()
	m.set("after-sign-in", map[string]any{"operation": "status", "call": status.ID})
	admittedTurn(t, h, c, conversation, "after-sign-in", "Observe after sign-in.")
	code, status := m.result()
	if code != "ok" || !status.Verified || p.count("auth") != 1 || p.signins.Load() != 2 {
		t.Fatal("sign-in resumed by duplicate send or lost result", code, status)
	}
	c.Close()
	h.stopCore(core)
}
