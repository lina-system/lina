// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	a2a "github.com/thisisjun786/lina/internal/core/a2a/publication"
	"github.com/thisisjun786/lina/internal/core/work"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestA2AConfigurationRefusesRunningCore(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err = configureA2A(root, []byte(`{"enabled":false}`), strings.NewReader("")); err == nil {
		t.Fatal("changed publication while another Core held its controls")
	}
	if _, err = os.Stat(filepath.Join(root, "system/core/a2a.json")); !os.IsNotExist(err) {
		t.Fatal("refused command wrote configuration")
	}
}
func TestA2ADisabledConfigurationNeedsNoCredential(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := configureA2A(root, []byte(`{"enabled":false}`), strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "system/core/a2a.json"))
	if err != nil || !strings.Contains(string(raw), `"enabled":false`) {
		t.Fatalf("disabled config: %s %v", raw, err)
	}
}

func TestA2ACredentialCannotMoveToAnotherIssuerOrClient(t *testing.T) {
	for _, changed := range []string{"issuer", "client"} {
		t.Run(changed, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			ctl, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			if err = ctl.Write("main.json", []byte(`{"main_id":"fixture"}`)); err != nil {
				t.Fatal(err)
			}
			ctl.Close()
			cfg := a2a.Config{Enabled: true, Address: "100.100.0.1:9443", Certificate: "/fixture/cert", PrivateKey: "/fixture/key", OAuth: a2a.OAuth{Issuer: "https://issuer.example", IntrospectionURL: "https://issuer.example/introspect", Audience: "a2a", ClientID: "core", SecretName: "issuer", Scopes: []string{"tasks"}}, Publications: []a2a.Publication{{ID: "p", Agent: "agent", Conversation: "main:agent", Name: "Task", Description: "Task", URL: "https://core.example/a2a/p", Definitions: []a2a.Definition{{ID: "d", Name: "Task", Description: "Task", Seconds: 60, MaxCalls: 2, Tools: []a2a.Tool{{Name: "read_record", Arguments: json.RawMessage(`{"id":"record"}`)}}, Work: work.WorkerDefinition{What: "Task", Done: "Result", Repository: "/fixture/repo", WriteArea: []string{"result.txt"}}, Artifact: a2a.ArtifactRule{Path: "result.txt", MediaType: "text/plain", Contains: "result", MaxBytes: 100}}}, Bindings: []a2a.Binding{{Subject: "caller", Client: "caller", Definitions: []string{"d"}, Expires: 2000000000, MaxActive: 1}}}}}
			original, _ := json.Marshal(cfg)
			if err = configureA2A(root, original, strings.NewReader("synthetic-secret\n")); err != nil {
				t.Fatal(err)
			}
			if changed == "issuer" {
				cfg.OAuth.Issuer = "https://other.example"
				cfg.OAuth.IntrospectionURL = "https://other.example/introspect"
			} else {
				cfg.OAuth.ClientID = "other-client"
			}
			next, _ := json.Marshal(cfg)
			if err = configureA2A(root, next, strings.NewReader("")); err == nil {
				t.Fatal("reused an introspection credential outside its configured issuer/client")
			}
			stored, err := os.ReadFile(filepath.Join(root, "system/core/a2a.json"))
			if err != nil || !bytes.Equal(original, stored) {
				t.Fatal("refused setup changed the active configuration", err)
			}
		})
	}
}
