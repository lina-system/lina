// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/tools"
)

type peerFixture struct {
	service    *Service
	connection Connection
	owner      Origin
	mode       string
	sends      atomic.Int32
	reads      atomic.Int32
	token      string
	changed    atomic.Bool
	revoked    atomic.Bool
}

func peer(t *testing.T, mode string) *peerFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vault.Close() })
	f := &peerFixture{mode: mode, token: "fixture-only-bearer", owner: Origin{Person: "owner", Agent: "agent", Conversation: "main:agent", Turn: "turn", Main: "main", Epoch: 1}}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("A2A-Version") != "1.0" {
			t.Error("missing wire version")
		}
		if r.URL.Path == "/card" {
			name := "controlled"
			if f.changed.Load() {
				name = "changed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "description": "Controlled artifact operation", "version": "1.0.0", "skills": []any{map[string]any{"id": "artifact", "name": "Artifact", "description": "Create a controlled artifact", "tags": []string{"artifact"}}}, "supportedInterfaces": []Interface{{URL: server.URL + "/rpc", Binding: "JSONRPC", Version: "1.0", Tenant: "tenant"}}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "capabilities": map[string]any{"streaming": true}, "securitySchemes": map[string]any{"key": map[string]any{"httpAuthSecurityScheme": map[string]any{"scheme": "Bearer"}}}, "securityRequirements": []any{map[string]any{"schemes": map[string]any{"key": map[string]any{"list": []string{}}}}}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			t.Error("missing credential at admitted peer")
		}
		if r.URL.Path == "/artifact" {
			_, _ = w.Write([]byte("verified result"))
			return
		}
		var in struct {
			ID, Method string
			Params     struct {
				Tenant        string
				Message       Message
				ID            string
				Configuration struct {
					Return bool `json:"returnImmediately"`
				}
			}
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("invalid RPC")
			return
		}
		if in.Params.Tenant != "tenant" {
			t.Error("tenant lost")
		}
		task := Task{ID: "remote-task", Context: "remote-context", Status: Status{State: "TASK_STATE_COMPLETED"}, Artifacts: []Artifact{{ID: "artifact", Parts: []Part{{URL: server.URL + "/artifact", MediaType: "text/plain"}}}}}
		if f.mode == "short-ids" {
			task.ID = "task-task"
			task.Context = "context-task"
		}
		var result any = task
		switch in.Method {
		case "SendMessage", "SendStreamingMessage":
			f.sends.Add(1)
			if !in.Params.Configuration.Return || in.Params.Message.Role != "ROLE_USER" || in.Params.Message.ID == "" {
				t.Error("bad send profile")
			}
			if in.Params.Message.Parts[0].Text == nil || *in.Params.Message.Parts[0].Text != "compute" {
				t.Error("payload changed")
			}
			// The observable operation happens even if the acknowledgment is lost.
			if err := os.WriteFile(filepath.Join(root, "peer-effect"), []byte("verified result"), 0600); err != nil {
				t.Error(err)
			}
			if f.mode == "lost" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			switch f.mode {
			case "direct":
				text := "verified result"
				result = map[string]any{"message": Message{ID: "direct", Context: "direct-context", Role: "ROLE_AGENT", Parts: []Part{{Text: &text}}}}
			case "claim":
				task.Artifacts = nil
				result = map[string]any{"task": task}
			case "input":
				task.Status.State = "TASK_STATE_INPUT_REQUIRED"
				task.Artifacts = nil
				result = map[string]any{"task": task}
			case "auth":
				task.Status.State = "TASK_STATE_AUTH_REQUIRED"
				task.Artifacts = nil
				result = map[string]any{"task": task}
			case "identity-leak":
				task.ID = f.token
				result = map[string]any{"task": task}
			case "leak":
				text := f.token
				task.Artifacts = []Artifact{{ID: "leak", Parts: []Part{{Text: &text}}}}
				result = map[string]any{"task": task}
			default:
				result = map[string]any{"task": task}
			}
			if in.Method == "SendStreamingMessage" {
				task.Status.State = "TASK_STATE_WORKING"
				task.Artifacts = nil
				w.Header().Set("Content-Type", "text/event-stream")
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": map[string]any{"task": task}})
				fmt.Fprintf(w, "data: %s\n\n", raw)
				w.(http.Flusher).Flush()
				return
			}
		case "GetTask":
			f.reads.Add(1)
		case "CancelTask":
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		default:
			t.Errorf("unexpected method %s", in.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	f.service = &Service{DB: db, Vault: vault, Epoch: 1, Network: Network{TLS: &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}}, Check: func(context.Context, Origin, Connection, Profile) error {
		if f.revoked.Load() {
			return ErrScope
		}
		return nil
	}}
	f.connection, err = f.service.Configure(t.Context(), Connection{ID: "peer", Person: "owner", CardURL: server.URL + "/card", Origin: server.URL, PrivateHost: "127.0.0.1", Scheme: "key", Profiles: []Profile{{Name: "compute", Class: tools.Calculation, MaxInput: 100, MaxOutput: 4096, SHA256: hash([]byte("verified result")), MediaType: "text/plain"}}}, []byte(f.token))
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *peerFixture) send(t *testing.T, stream bool) (Call, error) {
	t.Helper()
	return f.service.Send(t.Context(), f.owner, "stable-call", Input{Connection: "peer", Task: "compute", Text: "compute", Stream: stream})
}
func TestPeerResults(t *testing.T) {
	for _, mode := range []string{"task", "direct", "stream", "claim", "input", "auth", "leak"} {
		t.Run(mode, func(t *testing.T) {
			f := peer(t, mode)
			call, err := f.send(t, mode == "stream")
			if mode == "leak" {
				if err == nil || call.Verified {
					t.Fatal("credential accepted as evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "task", "direct", "stream":
				if !call.Verified || len(call.Evidence) != 1 || string(call.Evidence[0].Body) != "verified result" {
					t.Fatalf("no verified evidence: %+v", call)
				}
			case "claim":
				if call.Verified || call.State != "unverified" {
					t.Fatal("completion claim accepted")
				}
			case "input":
				if call.State != "input_required" {
					t.Fatal(call.State)
				}
			case "auth":
				if call.State != "auth_required" {
					t.Fatal(call.State)
				}
			}
			loaded, err := f.service.Call(t.Context(), f.owner, call.ID)
			if err != nil || loaded.State != call.State {
				t.Fatal("call not durable", err)
			}
			if mode == "stream" && f.reads.Load() != 1 {
				t.Fatal("interrupted stream not polled")
			}
		})
	}
}
func TestUncertainSendNeverRepeats(t *testing.T) {
	f := peer(t, "lost")
	call, err := f.send(t, false)
	if !errors.Is(err, ErrUnknown) || call.State != "unknown" {
		t.Fatal(call.State, err)
	}
	restarted := &Service{DB: f.service.DB, Vault: f.service.Vault, Network: f.service.Network, Epoch: 1, Check: f.service.Check}
	f.service = restarted
	for range 2 {
		again, _ := f.send(t, false)
		if again.State != "unknown" {
			t.Fatal("lost unknown")
		}
	}
	if f.sends.Load() != 1 {
		t.Fatal("uncertain send repeated")
	}
	o := f.owner
	o.Conversation = "other"
	if _, err = f.service.Call(t.Context(), o, call.ID); !errors.Is(err, ErrScope) {
		t.Fatal("foreign call visible")
	}
}
func TestAuthorityAndCardFence(t *testing.T) {
	for _, mode := range []string{"card", "revoke", "authority"} {
		t.Run(mode, func(t *testing.T) {
			f := peer(t, "task")
			switch mode {
			case "card":
				f.changed.Store(true)
			case "revoke":
				if err := f.service.Revoke(t.Context(), "owner", "peer"); err != nil {
					t.Fatal(err)
				}
			case "authority":
				f.revoked.Store(true)
			}
			if _, err := f.send(t, false); err == nil || f.sends.Load() != 0 {
				t.Fatal("fenced send reached peer")
			}
		})
	}
}
func TestUncertainCancellation(t *testing.T) {
	f := peer(t, "input")
	call, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	call, err = f.service.Observe(t.Context(), f.owner, call.ID, false, true)
	if err == nil || call.State != "cancellation_unknown" || call.Verified {
		t.Fatal(call.State, err)
	}
	if f.sends.Load() != 1 {
		t.Fatal("cancel sent new work")
	}
}
func TestNoCredentialInDurableEvidence(t *testing.T) {
	for _, mode := range []string{"leak", "identity-leak"} {
		t.Run(mode, func(t *testing.T) {
			f := peer(t, mode)
			_, _ = f.send(t, false)
			calls, err := f.service.Undelivered(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(calls)
			if strings.Contains(string(raw), f.token) {
				t.Fatal("credential persisted")
			}
		})
	}
}

func TestUncertainSendReservesWorkAcrossNewCallIDs(t *testing.T) {
	f := peer(t, "lost")
	first, _ := f.send(t, false)
	_, _ = f.service.Send(t.Context(), f.owner, "different-model-call", Input{Connection: "peer", Task: "compute", Text: "compute"})
	if f.sends.Load() != 1 {
		t.Fatalf("uncertain work repeated with a new model call id: first=%s sends=%d", first.ID, f.sends.Load())
	}
}

func TestObservationKeepsOriginalTurn(t *testing.T) {
	f := peer(t, "input")
	original, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	later := f.owner
	later.Turn = "later-observation"
	observed, err := f.service.Observe(t.Context(), later, original.ID, false, false)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := f.service.Call(t.Context(), f.owner, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Origin.Turn != original.Origin.Turn || retained.Origin.Turn != original.Origin.Turn {
		t.Fatal("observation changed result provenance")
	}
}

func TestMultipartBinaryEvidence(t *testing.T) {
	f := peer(t, "task")
	call, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte{0xff, 0, 1, 2, 3}
	call.Profile.MediaType = "application/octet-stream"
	call.Profile.SHA256 = hash(body)
	first, second := base64.StdEncoding.EncodeToString(body[:2]), base64.StdEncoding.EncodeToString(body[2:])
	artifacts := []Artifact{{ID: "binary", Parts: []Part{{Raw: &first, MediaType: call.Profile.MediaType}, {Raw: &second, MediaType: call.Profile.MediaType}}}}
	if err = f.service.verify(t.Context(), f.connection, &call, artifacts); err != nil || !call.Verified || len(call.Evidence) != 1 || !bytes.Equal(call.Evidence[0].Body, body) {
		t.Fatal("binary parts not retained and checked", err)
	}
}

func TestOpaqueTaskIDsAreNotJoinedForSecretDetection(t *testing.T) {
	f := peer(t, "short-ids")
	call, err := f.send(t, false)
	if err != nil || call.TaskID != "task-task" || call.ContextID != "context-task" || !call.Verified {
		t.Fatal("valid opaque identities refused", err)
	}
}

func TestOAuthSettingsScopesRoundTrip(t *testing.T) {
	var o OAuth
	if err := json.Unmarshal([]byte(`{"issuer":"https://issuer.test","scopes":["task"]}`), &o); err != nil || len(o.Scopes) != 1 {
		t.Fatal("owner OAuth scopes lost", err)
	}
}

func TestOwnerReviewCanAdmitAnUnpredictableArtifact(t *testing.T) {
	f := peer(t, "task")
	connection := f.connection
	connection.Profiles[0].SHA256 = ""
	if _, err := f.service.Configure(t.Context(), connection, nil); err != nil {
		t.Fatal("owner-reviewed task cannot be configured", err)
	}
	call, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	if call.Verified || len(call.Evidence) != 1 || call.State != "unverified" {
		t.Fatal("remote claim accepted before owner verification")
	}
}

func TestOwnerVerificationBindsRetainedArtifactAndPerson(t *testing.T) {
	f := peer(t, "task")
	connection := f.connection
	connection.Profiles[0].SHA256 = ""
	if _, err := f.service.Configure(t.Context(), connection, nil); err != nil {
		t.Fatal(err)
	}
	call, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	foreign := f.owner
	foreign.Person = "other"
	if _, err = f.service.Accept(t.Context(), foreign, call.ID, call.Evidence[0].SHA256); err == nil {
		t.Fatal("foreign owner accepted output")
	}
	if _, err = f.service.Accept(t.Context(), f.owner, call.ID, hash([]byte("different"))); err == nil {
		t.Fatal("changed artifact accepted")
	}
	accepted, err := f.service.Accept(t.Context(), f.owner, call.ID, call.Evidence[0].SHA256)
	if err != nil || !accepted.Verified || accepted.AcceptedBy != f.owner.Person || accepted.VerifiedHash != call.Evidence[0].SHA256 {
		t.Fatal("owner verification not retained", err)
	}
	if _, err = f.service.Accept(t.Context(), f.owner, call.ID, call.Evidence[0].SHA256); err == nil {
		t.Fatal("late verification accepted")
	}
	if f.sends.Load() != 1 {
		t.Fatal("verification sent work")
	}
}

func TestTerminalTaskCannotAskForAnotherSend(t *testing.T) {
	f := peer(t, "task")
	call, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	err = f.service.observeTask(t.Context(), f.connection, &call, Task{ID: call.TaskID, Context: call.ContextID, Status: Status{State: "TASK_STATE_INPUT_REQUIRED"}})
	if err == nil || call.State == "input_required" {
		t.Fatal("terminal task reopened by remote status")
	}
}

func TestHTTPBearerSchemeIsCaseInsensitive(t *testing.T) {
	f := peer(t, "task")
	c := f.connection
	c.Card.SecuritySchemes["key"] = json.RawMessage(`{"httpAuthSecurityScheme":{"scheme":"bearer"}}`)
	header, prefix, err := authentication(c)
	if err != nil || header != "Authorization" || prefix != "Bearer " {
		t.Fatal("HTTP auth scheme case rejected", err)
	}
}

func TestRequiredAgentCardFields(t *testing.T) {
	f := peer(t, "task")
	base, _ := json.Marshal(f.connection.Card)
	for _, field := range []string{"name", "description", "version", "capabilities", "supportedInterfaces", "defaultInputModes", "defaultOutputModes", "skills"} {
		t.Run(field, func(t *testing.T) {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(base, &fields)
			delete(fields, field)
			raw, _ := json.Marshal(fields)
			var card Card
			_ = json.Unmarshal(raw, &card)
			if validCardShape(card) {
				t.Fatal("required Agent Card field omitted")
			}
		})
	}
}

func TestUncertainTaskCannotFallbackWithChangedInput(t *testing.T) {
	f := peer(t, "lost")
	_, _ = f.send(t, false)
	_, _ = f.service.Send(t.Context(), f.owner, "new-intent-in-same-task", Input{Connection: "peer", Task: "compute", Text: "compute differently"})
	if f.sends.Load() != 1 {
		t.Fatal("unknown task sent another operation under changed input")
	}
}

func TestEmptyRawArtifactIsRetained(t *testing.T) {
	f := peer(t, "task")
	call, err := f.send(t, false)
	if err != nil {
		t.Fatal(err)
	}
	call.Profile.MediaType = "application/octet-stream"
	call.Profile.SHA256 = hash(nil)
	var artifacts []Artifact
	if err = json.Unmarshal([]byte(`[{"artifactId":"empty","parts":[{"raw":"","mediaType":"application/octet-stream"}]}]`), &artifacts); err != nil {
		t.Fatal(err)
	}
	if err = f.service.verify(t.Context(), f.connection, &call, artifacts); err != nil || !call.Verified {
		t.Fatal("empty bytes treated as absent part", err)
	}
}
