// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/update"
)

// The test service manager runs outside the updater's process tree. Every
// product invocation remains the real installed CLI, Core and worker binary.
func updateManagerHelper() bool {
	socket := os.Getenv("LINA_UPDATE_MANAGER_SOCKET")
	if socket == "" || len(os.Args) < 2 || os.Args[1] != "--user" {
		return false
	}
	c, err := net.Dial("unix", socket)
	if err != nil {
		os.Exit(95)
	}
	defer c.Close()
	_ = json.NewEncoder(c).Encode(os.Args[1:])
	var reply struct {
		Text string
		Code int
	}
	if json.NewDecoder(c).Decode(&reply) != nil {
		os.Exit(96)
	}
	fmt.Print(reply.Text)
	os.Exit(reply.Code)
	return true
}
func updateManager(t *testing.T, h *harness, start func(bool) *process, initial *process) (string, string, func() *process) {
	t.Helper()
	base := filepath.Dir(h.root)
	socket := filepath.Join(base, "manager.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	current := initial
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			var args []string
			err = json.NewDecoder(c).Decode(&args)
			reply := struct {
				Text string
				Code int
			}{}
			if err != nil || len(args) < 2 {
				reply.Code = 97
			} else {
				switch args[1] {
				case "show":
					if strings.Contains(strings.Join(args, " "), "FragmentPath") {
						reply.Text = filepath.Join(base, "units/lina-core.service") + "\n"
					}
				case "stop":
					mu.Lock()
					p := current
					mu.Unlock()
					if p != nil {
						select {
						case <-p.done:
						default:
							reply.Code = 98
						}
					}
				case "start":
					p := start(true)
					mu.Lock()
					current = p
					mu.Unlock()
				default:
					reply.Code = 99
				}
			}
			_ = json.NewEncoder(c).Encode(reply)
			c.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "update-tools")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(binary, filepath.Join(dir, "systemctl")); err != nil {
		t.Fatal(err)
	}
	return dir, socket, func() *process { mu.Lock(); defer mu.Unlock(); return current }
}
func addUpdateWorker(t *testing.T, bundle string) {
	t.Helper()
	data, err := os.ReadFile(buildCommand(t, "moirai-worker"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "moirai-worker"), data, 0700); err != nil {
		t.Fatal(err)
	}
	alterUpdateManifest(t, bundle, func(m map[string]any) {
		m["binaries"] = append(m["binaries"].([]any), map[string]any{"name": "moirai-worker", "size": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
	})
}
func alterUpdateManifest(t *testing.T, bundle string, change func(map[string]any)) {
	t.Helper()
	path := filepath.Join(bundle, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	data, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestM2UpdateActivation(t *testing.T) {
	data, err := os.ReadFile("../m2/update-activation.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []string `json:"cases"`
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	real := buildLina(t)
	node := buildCommand(t, "lina-node")
	bundle := makeInstallBundle(t, t.TempDir(), real, node)
	addUpdateWorker(t, bundle)
	candidate := makeInstallBundle(t, t.TempDir(), real, node)
	addUpdateWorker(t, candidate)
	alterUpdateManifest(t, candidate, func(m map[string]any) { m["version"] = "0.0.1-alpha.20261022.1" })
	archive, digest := packUpdate(t, candidate)
	broken := makeInstallBundle(t, t.TempDir(), real, node)
	addUpdateWorker(t, broken)
	script := []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nif [ \"$2\" = --preflight ]; then exec '" + real + "' \"$@\"; fi\nexit 78\n")
	if err = os.WriteFile(filepath.Join(broken, "lina"), script, 0700); err != nil {
		t.Fatal(err)
	}
	alterUpdateManifest(t, broken, func(m map[string]any) {
		for _, v := range m["binaries"].([]any) {
			b := v.(map[string]any)
			if b["name"] == "lina" {
				b["size"] = len(script)
				b["sha256"] = fmt.Sprintf("%x", sha256.Sum256(script))
			}
		}
	})
	brokenArchive, brokenDigest := packUpdate(t, broken)
	for _, failure := range scenario.Cases {
		t.Run(failure, func(t *testing.T) {
			smokeReached := make(chan struct{})
			releaseProvider := make(chan struct{})
			var calls atomic.Int32
			var toolResult atomic.Bool
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if failure == "after-ready" {
					w.WriteHeader(503)
					return
				}
				if failure == "killed-after-ready" {
					io.Copy(io.Discard, r.Body)
					close(smokeReached)
					select {
					case <-r.Context().Done():
					case <-releaseProvider:
					}
					return
				}
				if failure == "success" && n == 2 {
					emitPersonaCall(w, "after-update-persona", "change_persona", map[string]any{"quote": "talk more casually", "user_voice": "update fixture casual voice"})
					return
				}
				if failure == "success" && n == 3 {
					var request struct{ Input []json.RawMessage }
					if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
						t.Error(e)
					} else if len(request.Input) > 0 {
						var result struct{ Type, Output string }
						json.Unmarshal(request.Input[len(request.Input)-1], &result)
						toolResult.Store(result.Type == "function_call_output" && strings.Contains(result.Output, `"code":"ok"`))
					}
				}
				emitResponse(w, "Update conversation completed.", true)
			}))
			defer provider.Close()
			defer close(releaseProvider)
			h := newHarness(t, real)
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			h.ctx = ctx
			h, start := installConversationBundle(t, h, bundle, provider.URL)
			core := start(true)
			tools, manager, current := updateManager(t, h, start, core)
			source, sha := archive, digest
			if failure == "before-ready" {
				source, sha = brokenArchive, brokenDigest
			}
			p := h.command("update", "--prepare", "--state-root", h.root, "--archive", source, "--sha256", sha)
			p.start(t)
			p.wait(t, 0)
			var prepared update.Prepared
			if err = json.Unmarshal(p.stdout.Bytes(), &prepared); err != nil {
				t.Fatal(err)
			}
			command := func(args ...string) *process {
				p := h.command(args...)
				p.command.Env = append(p.command.Env, "PATH="+tools, "LINA_UPDATE_MANAGER_SOCKET="+manager, "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
				return p
			}
			activate := command("update", "--activate", "--state-root", h.root, "--candidate", prepared.Directory)
			activate.start(t)
			code := 0
			if failure != "success" {
				code = 78
			}
			if failure == "killed-after-ready" {
				select {
				case <-smokeReached:
				case <-ctx.Done():
					t.Fatal("candidate never dispatched smoke")
				}
				activate.killAndWait()
				// kit/proc joins the killed owner's candidate. The installed Core lock,
				// rather than elapsed time, is the evidence that recovery may now proceed.
				for {
					s, e := ctlfiles.Open(h.root)
					if e != nil {
						t.Fatal(e)
					}
					e = s.Acquire()
					s.Close()
					if e == nil {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(e)
					case <-time.After(10 * time.Millisecond):
					}
				}
				activate = command("update", "--resume", "--state-root", h.root)
				activate.start(t)
			}
			select {
			case <-activate.done:
			case <-ctx.Done():
				activate.killAndWait()
				t.Fatalf("activation did not finish: %s", &activate.stderr)
			}
			activate.wait(t, code)
			var receipt ctlfiles.UpdateRecord
			if err = json.Unmarshal(activate.stdout.Bytes(), &receipt); err != nil {
				t.Fatalf("%v: %s %s", err, &activate.stdout, &activate.stderr)
			}
			switch failure {
			case "success":
				if receipt.Phase != "complete" || !receipt.ReachedReady || receipt.Generation == "" {
					t.Fatalf("incomplete update: %+v", receipt)
				}
				c, conversation := conversationClient(t, h)
				admittedTurn(t, h, c, conversation, "after-update", "Please talk more casually after the update.")
				c.Close()
				if !toolResult.Load() {
					t.Fatal("resumed Core did not execute the persona tool and return its receipt")
				}
				h.stopCore(current())
			case "before-ready":
				if receipt.Phase != "rolled_back" || receipt.ReachedReady || receipt.Generation == "" {
					t.Fatalf("no pre-ready rollback: %+v stderr=%s", receipt, &activate.stderr)
				}
				c, conversation := conversationClient(t, h)
				admittedTurn(t, h, c, conversation, "after-rollback", "Confirm the restored Core can answer.")
				c.Close()
				h.stopCore(current())
			case "after-ready", "killed-after-ready":
				if receipt.Phase != "held" || !receipt.ReachedReady || receipt.Cause == "" {
					t.Fatalf("post-ready failure rolled back: %+v stderr=%s", receipt, &activate.stderr)
				}
				refused := h.command("core", "--state-root", h.root, "--socket", h.socket)
				refused.start(t)
				refused.wait(t, 78)
				if !strings.Contains(refused.stderr.String(), "update_held") {
					t.Fatal("hold missing closed cause")
				}
			}
		})
	}
}
