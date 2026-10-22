// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2GitHubMergeInstalledCore(t *testing.T) {
	var f struct {
		Repository, Base, Head string
		Number                 string `json:"pr_number"`
		Moved                  string `json:"moved_head"`
		Commit                 string `json:"merge_commit"`
		MovedCause             string `json:"moved_cause"`
		ProtectedCause         string `json:"protected_cause"`
		ObservationCause       string `json:"observation_cause"`
	}
	raw, err := os.ReadFile("m2/github-merge.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	// Two boots and several real Git/gh child lifecycles share a bounded scenario budget.
	scenarioContext, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	t.Cleanup(cancel)
	h.ctx = scenarioContext
	dir := filepath.Join(h.root, "system", "core")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(h.root, "clone")
	if err = os.Mkdir(clone, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", clone}, args...)...)
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("remote", "add", "origin", "https://github.com/"+f.Repository+".git")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base")
	baseline := git("rev-parse", "HEAD")
	phase := func(value string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(h.root, "phase"), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	phase("open")
	script := `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
printf '%s\n' "$*" >> "$LINA_GH_FIXTURE/calls"
phase=$(/bin/cat "$LINA_GH_FIXTURE/phase")
head=HEAD_SHA
if [ "$phase" = moved ]; then head=MOVED_SHA; fi
if [ "$1" = pr ]; then
 printf merged > "$LINA_GH_FIXTURE/phase"
 exit 0
fi
printf 'HTTP/2.0 200 OK\r\n\r\n'
case "$*" in
 *graphql*) printf '{"data":{"repository":{"pullRequest":{"headRefOid":"%s","reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}' "$head" ;;
 *check-runs*)
   conclusion=success; if [ "$phase" = observed ]; then conclusion=failure; fi
   printf '{"total_count":1,"check_runs":[{"name":"foundation","status":"completed","conclusion":"%s"}]}' "$conclusion" ;;
 */status*)
   case "$*" in *MERGE_SHA*) head=MERGE_SHA;; esac
   printf '{"sha":"%s","total_count":0,"statuses":[]}' "$head" ;;
 *git/ref/heads/dev*)
   target=MERGE_SHA; if [ "$phase" = bad_target ]; then target=MOVED_SHA; fi
   printf '{"ref":"refs/heads/dev","object":{"type":"commit","sha":"%s"}}' "$target" ;;
 *commits/MERGE_SHA*) printf '{"sha":"MERGE_SHA"}' ;;
 *pulls/140*)
   merged=false; state=open
   case "$phase" in merged|bad_target|observed) merged=true; state=closed;; esac
   printf '{"number":140,"state":"%s","merged":%s,"head":{"sha":"%s"},"base":{"ref":"dev","repo":{"full_name":"REPOSITORY"}},"merge_commit_sha":"MERGE_SHA"}' "$state" "$merged" "$head" ;;
 *) exit 90 ;;
esac
`
	script = strings.NewReplacer("HEAD_SHA", f.Head, "MOVED_SHA", f.Moved, "MERGE_SHA", f.Commit, "REPOSITORY", f.Repository).Replace(script)
	if err = os.WriteFile(filepath.Join(h.root, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(gitPath, filepath.Join(h.root, "git")); err != nil {
		t.Fatal(err)
	}
	start := func() (*process, *client) {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "PATH="+h.root, "LINA_GH_FIXTURE="+h.root)
		core.start(t)
		conn := h.connect(core)
		c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
		hello := clientHello([]string{"1.0"})
		hello.Capabilities = []protocol.Capability{{Name: "github", Version: "1.0"}, {Name: "github_merge", Version: "1.0"}}
		answer := c.call(t, protocol.KindHello, hello)
		welcome, ok := answer.Payload.(protocol.Welcome)
		if !ok {
			t.Fatalf("hello: %+v", answer)
		}
		c.welcome = welcome
		if !slices.Contains(welcome.Capabilities, protocol.Capability{Name: "github_merge", Version: "1.0"}) {
			t.Fatal("running Core has no merge capability")
		}
		h.inputRefused(c)
		return core, c
	}
	core, c := start()
	call := func(p protocol.WorkRequest) protocol.WorkResult {
		t.Helper()
		answer := c.callWithin(t, protocol.KindWorkRequest, p, 60*time.Second)
		if answer.Error != nil {
			t.Fatalf("request %+v: %+v", p, answer.Error)
		}
		r, ok := answer.Payload.(protocol.WorkResult)
		if !ok {
			t.Fatalf("result: %+v", answer)
		}
		return r
	}
	ptr := func(s string) *string { return &s }
	request := func(op, id string) protocol.WorkRequest {
		return protocol.WorkRequest{Operation: ptr(op), BindingID: ptr(id)}
	}
	bind := func(id string) {
		t.Helper()
		p := request("github_bind", id)
		p.Repository = &f.Repository
		p.PrNumber = &f.Number
		p.Base = &f.Base
		p.Clone = &clone
		refs := []string{"refs/heads/dev"}
		p.ProtectedRefs = &refs
		if len(c.welcome.Agents) == 0 {
			t.Fatal("installed Core has no agent")
		}
		p.AgentID = &c.welcome.Agents[0].AgentID
		r := call(p)
		if r.State == nil || *r.State != "bound" {
			t.Fatalf("bind: %+v", r)
		}
	}
	accept := func(id, head string) {
		t.Helper()
		p := request("github_accept", id)
		p.Head = &head
		r := call(p)
		if r.State == nil || *r.State != "ready" {
			t.Fatalf("accept: %+v", r)
		}
	}
	noWrites := func() {
		t.Helper()
		raw, e := os.ReadFile(filepath.Join(h.root, "calls"))
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(raw), "pr merge") {
			t.Fatalf("unexpected write: %s", raw)
		}
	}
	bind("task")
	accept("task", f.Head)
	phase("moved")
	r := call(request("github_merge", "task"))
	if r.Cause == nil || *r.Cause != f.MovedCause || r.Head == nil || *r.Head != f.Moved {
		t.Fatalf("moved: %+v", r)
	}
	noWrites()
	phase("open")
	accept("task", f.Head)
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "outside change")
	r = call(request("github_merge", "task"))
	if r.Cause == nil || *r.Cause != f.ProtectedCause {
		t.Fatalf("protected: %+v", r)
	}
	noWrites()
	git("update-ref", "refs/heads/dev", baseline)
	accept("task", f.Head)
	r = call(request("github_merge", "task"))
	if r.State == nil || *r.State != "merged" || r.MergeCommit == nil || *r.MergeCommit != f.Commit || r.Source == nil || *r.Source != "receipt" {
		t.Fatalf("merge: %+v", r)
	}
	h.stopCore(core)
	core, c = start()
	r = call(request("github_merge", "task"))
	if r.MergeCommit == nil || *r.MergeCommit != f.Commit || *r.State != "merged" {
		t.Fatalf("durable replay: %+v", r)
	}
	bind("person-merge")
	phase("bad_target")
	r = call(request("github_observe_merge", "person-merge"))
	if r.State != nil && *r.State == "merged" {
		t.Fatal("accepted wrong target")
	}
	phase("observed")
	r = call(request("github_observe_merge", "person-merge"))
	if r.State == nil || *r.State != "merged" || r.Source == nil || *r.Source != "observed" || r.Cause == nil || *r.Cause != f.ObservationCause {
		t.Fatalf("observed: %+v", r)
	}
	h.stopCore(core)
	calls, e := os.ReadFile(filepath.Join(h.root, "calls"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(calls), "pr merge") != 1 || !strings.Contains(string(calls), "--match-head-commit "+f.Head) {
		t.Fatalf("writes: %s", calls)
	}
	if strings.Contains(string(calls), "--admin") || strings.Contains(string(calls), "--auto") {
		t.Fatal("merge bypass or implicit auto-merge")
	}
}
