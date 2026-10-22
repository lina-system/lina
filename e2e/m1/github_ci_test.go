// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2GitHubCIInstalledCore(t *testing.T) {
	t.Parallel()

	var fixture struct {
		Repository   string
		Number       string `json:"pr_number"`
		Head         string
		InitialCI    string `json:"initial_ci"`
		InitialCause string `json:"initial_cause"`
		FinalCI      string `json:"final_ci"`
	}
	data, err := os.ReadFile("../m2/github-ci.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	dir := filepath.Join(h.root, "system", "core")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
set -eu
printf '%s\n' "$*" >> "$LINA_GH_FIXTURE/calls"
if [ ! -f "$LINA_GH_FIXTURE/limited" ]; then
 : > "$LINA_GH_FIXTURE/limited"
 printf 'HTTP/2.0 403 Forbidden\r\nRetry-After: 1\r\nX-Poll-Interval: 1\r\n\r\n{"message":"secondary rate limit"}'
 exit 1
fi
case "$*" in
 *graphql*) printf 'HTTP/2.0 200 OK\r\n\r\n{"data":{"repository":{"pullRequest":{"headRefOid":"HEAD","reviewThreads":{"nodes":[{"id":"thread-1","isResolved":false,"isOutdated":false,"comments":{"nodes":[{"id":"comment-1","body":"synthetic review data","path":"a.go","url":"https://github.com/fixture/repository/pull/140"}],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":false}}}}}}' ;;
 *check-runs*)
   if [ -f "$LINA_GH_FIXTURE/finish" ]; then state=completed; conclusion=success; else state=in_progress; conclusion=unknown; fi
   printf 'HTTP/2.0 200 OK\r\nETag: "checks"\r\nX-Poll-Interval: 1\r\n\r\n{"total_count":1,"check_runs":[{"name":"foundation","status":"%s","conclusion":"%s"}]}' "$state" "$conclusion" ;;
 */status*) printf 'HTTP/2.0 200 OK\r\nETag: "statuses"\r\nX-Poll-Interval: 1\r\n\r\n{"sha":"HEAD","total_count":0,"statuses":[]}' ;;
 *pulls/140*) printf 'HTTP/2.0 200 OK\r\nETag: "pr"\r\nX-Poll-Interval: 1\r\n\r\n{"number":140,"state":"open","merged":false,"head":{"sha":"HEAD"}}' ;;
 *) exit 90 ;;
esac
`
	script = strings.ReplaceAll(script, "HEAD", fixture.Head)
	if err = os.WriteFile(filepath.Join(h.root, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+h.root+":/usr/bin:/bin", "LINA_GH_FIXTURE="+h.root)
	core.start(t)
	connect := func(caps []protocol.Capability) *client {
		conn := h.connect(core)
		c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
		hello := clientHello([]string{"1.0"})
		hello.Capabilities = append([]protocol.Capability{}, caps...)
		answer := c.call(t, protocol.KindHello, hello)
		welcome, ok := answer.Payload.(protocol.Welcome)
		if !ok {
			t.Fatalf("handshake: %+v", answer)
		}
		c.welcome = welcome
		return c
	}
	capability := protocol.Capability{Name: "github", Version: "1.0"}
	c := connect([]protocol.Capability{capability})
	if !slices.Contains(c.welcome.Capabilities, capability) {
		t.Fatal("running Core does not expose GitHub observations")
	}
	operation := "github_watch"
	request := protocol.WorkRequest{Operation: &operation, Repository: &fixture.Repository, PrNumber: &fixture.Number}
	unprivileged := connect(nil)
	denied := unprivileged.call(t, protocol.KindWorkRequest, request)
	if denied.Error == nil || denied.Error.Code != protocol.ErrorCapabilityNotNegotiated {
		t.Fatalf("unnegotiated read: %+v", denied)
	}
	read := func() protocol.WorkResult {
		answer := c.call(t, protocol.KindWorkRequest, request)
		if answer.Error != nil || answer.EffectState != protocol.EffectNone {
			t.Fatalf("observation: %+v", answer)
		}
		result, ok := answer.Payload.(protocol.WorkResult)
		if !ok || result.Ci == nil || result.Done == nil || result.Cause == nil {
			t.Fatalf("missing observation: %+v", answer)
		}
		return result
	}
	first := read()
	if *first.Ci != fixture.InitialCI || *first.Cause != fixture.InitialCause || *first.Done || first.RetryAt == nil {
		t.Fatalf("rate limit: %+v", first)
	}
	// Bounded polling observes protocol state; elapsed time never proves completion.
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		result := read()
		if result.Head != nil && *result.Head == fixture.Head && *result.Cause == "" {
			break
		}
		select {
		case <-poll.C:
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
	}
	if err = os.WriteFile(filepath.Join(h.root, "finish"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		result := read()
		if *result.Done {
			if *result.Ci != fixture.FinalCI || result.Head == nil || *result.Head != fixture.Head || result.Reviews == nil || len(*result.Reviews) != 1 {
				t.Fatalf("conclusion: %+v", result)
			}
			break
		}
		select {
		case <-poll.C:
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
	}
	h.stopCore(core)
	calls, err := os.ReadFile(filepath.Join(h.root, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), " graphql ") != 1 || !strings.Contains(string(calls), "If-None-Match:") {
		t.Fatalf("reads did not reuse snapshot/validators: %s", calls)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if !strings.HasPrefix(line, "api --hostname github.com --method ") || strings.Contains(line, "events") || strings.Contains(line, "webhook") {
			t.Fatalf("unexpected command: %s", line)
		}
	}
}
