// SPDX-License-Identifier: Apache-2.0

package ghclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

type testLeaser struct{}

func (testLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

type fakeResponse struct {
	code          int
	headers, body string
}
type fakeRunner struct {
	mu      sync.Mutex
	replies []fakeResponse
	args    [][]string
}

func (f *fakeRunner) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.args)
}
func (f *fakeRunner) Run(_ context.Context, _ lease.Lease, c proc.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.args = append(f.args, slices.Clone(c.Args))
	if len(f.replies) == 0 {
		return fmt.Errorf("unexpected read")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	fmt.Fprintf(c.Stdout, "HTTP/2.0 %d Status\r\n%s\r\n%s", r.code, r.headers, r.body)
	if r.code >= 400 {
		return fmt.Errorf("exit 1")
	}
	return nil
}
func response(state, status, conclusion, head string) string {
	checks := `{"nodes":[{"__typename":"CheckRun","name":"foundation","status":` + quote(status) + `,"conclusion":` + quote(conclusion) + `}],"pageInfo":{"hasNextPage":false}}`
	if status == "absent" {
		checks = `{"nodes":[],"pageInfo":{"hasNextPage":false}}`
	}
	return `{"data":{"repository":{"pullRequest":{"number":140,"state":` + quote(state) + `,"headRefOid":` + quote(head) + `,"reviewThreads":{"nodes":[{"id":"T1","isResolved":false,"isOutdated":false,"comments":{"nodes":[{"id":"C1","body":"review data","path":"a.go","url":"https://github.com/o/r/pull/140#x"}],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":false}},"commits":{"nodes":[{"commit":{"oid":` + quote(head) + `,"statusCheckRollup":{"contexts":` + checks + `}}}]}}}}}`
}
func bundle(state, status, conclusion, head string) []fakeResponse {
	pr := fakeResponse{200, "ETag: \"pr\"\r\nX-Poll-Interval: 7\r\n", fmt.Sprintf(`{"number":140,"state":%s,"merged":%t,"head":{"sha":%s}}`, quote(strings.ToLower(state)), state == "MERGED", quote(head))}
	if state == "MERGED" {
		pr.body = strings.Replace(pr.body, `"state":"merged"`, `"state":"closed"`, 1)
	}
	if state != "OPEN" {
		return []fakeResponse{pr}
	}
	checks := fmt.Sprintf(`{"total_count":1,"check_runs":[{"name":"foundation","status":%s,"conclusion":%s}]}`, quote(strings.ToLower(status)), quote(strings.ToLower(conclusion)))
	if status == "absent" {
		checks = `{"total_count":0,"check_runs":[]}`
	}
	return []fakeResponse{pr, {200, "", response(state, status, conclusion, head)}, {200, "ETag: \"checks\"\r\nX-Poll-Interval: 7\r\n", checks}, {200, "ETag: \"statuses\"\r\nX-Poll-Interval: 7\r\n", `{"sha":` + quote(head) + `,"total_count":0,"statuses":[]}`}}
}
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

const sha = "0123456789012345678901234567890123456789"

func testClient(t *testing.T, f *fakeRunner) *Client {
	t.Helper()
	c, err := New(t.Context(), Config{Path: "/usr/bin/gh", Env: []string{"HOME=/synthetic"}, Leaser: testLeaser{}})
	if err != nil {
		t.Fatal(err)
	}
	c.runner = f
	t.Cleanup(func() {
		if err := c.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return c
}
func TestWatchSingletonConditionalAndConclusion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replies := bundle("OPEN", "IN_PROGRESS", "", sha)
		for range 3 {
			replies = append(replies, fakeResponse{304, "X-Poll-Interval: 9\r\n", ""})
		}
		final := bundle("OPEN", "COMPLETED", "SUCCESS", sha)
		replies = append(replies, final[0])
		replies = append(replies, final[2:]...)
		f := &fakeRunner{replies: replies}
		c := testClient(t, f)
		ref := Ref{"o/r", 140}
		w, err := c.Watch(ref)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				other, e := c.Watch(Ref{"O/R", 140})
				if e != nil || other != w {
					t.Error("duplicate watcher", e)
				}
			})
		}
		wg.Wait()
		s, err := w.Next(t.Context(), 0)
		if err != nil || s.CI != "pending" || s.Head != sha || len(s.Reviews) != 1 {
			t.Fatalf("initial: %+v %v", s, err)
		}
		synctest.Wait()
		time.Sleep(7 * time.Second)
		synctest.Wait()
		f.mu.Lock()
		n := len(f.args)
		args := slices.Clone(f.args[len(f.args)-1])
		f.mu.Unlock()
		if n != 7 || !slices.Contains(args, `If-None-Match: "statuses"`) {
			t.Fatalf("conditional reads: %d %v", n, args)
		}
		time.Sleep(9 * time.Second)
		synctest.Wait()
		s, err = w.Next(t.Context(), s.Revision)
		if err != nil || !s.Done || s.CI != "success" {
			t.Fatalf("conclusion %+v %v", s, err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(f.calls()) != 10 {
			t.Fatalf("watch survived conclusion: %d reads", len(f.calls()))
		}
		if _, err = w.Next(t.Context(), s.Revision); err != io.EOF {
			t.Fatalf("terminal next: %v", err)
		}
	})
}
func TestRateLimitRemainsPendingAndBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeRunner{replies: append([]fakeResponse{{403, "Retry-After: 60\r\n", `{"message":"secondary rate limit"}`}}, bundle("OPEN", "COMPLETED", "FAILURE", sha)...)}
		c := testClient(t, f)
		w, _ := c.Watch(Ref{"o/r", 140})
		s, e := w.Next(t.Context(), 0)
		if e != nil || s.CI != "pending" || s.Cause != "rate_limited" || s.Done || s.RetryAt.IsZero() {
			t.Fatalf("rate limit: %+v %v", s, e)
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if len(f.calls()) != 1 {
			t.Fatal("retried before server deadline")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		s, e = w.Next(t.Context(), s.Revision)
		if e != nil || s.CI != "failure" || !s.Done || s.Cause != "" {
			t.Fatalf("recovery: %+v %v", s, e)
		}
	})
}
func TestClosedMergedAndMissingChecks(t *testing.T) {
	for _, state := range []string{"CLOSED", "MERGED", "OPEN"} {
		t.Run(state, func(t *testing.T) {
			f := &fakeRunner{replies: bundle(state, "absent", "", sha)}
			c := testClient(t, f)
			s, e := c.Read(t.Context(), Ref{"o/r", 140})
			if e != nil || s.CI != "pending" || s.Done != (state != "OPEN") {
				t.Fatalf("%+v %v", s, e)
			}
		})
	}
}
func TestRejectArgumentsBeforeRunning(t *testing.T) {
	f := &fakeRunner{}
	c := testClient(t, f)
	for _, ref := range []Ref{{"--help", 1}, {"o/r/../../x", 1}, {"o/r\nX: y", 1}, {"o/r", 0}, {"https://evil/o/r", 1}} {
		if _, e := c.Read(t.Context(), ref); e == nil {
			t.Fatalf("accepted %+v", ref)
		}
	}
	if len(f.calls()) != 0 {
		t.Fatal("invalid input reached child")
	}
}
func TestFixedReadArgumentsAndNoRawDiagnostics(t *testing.T) {
	f := &fakeRunner{replies: []fakeResponse{{401, "", `{"message":"synthetic-secret"}`}}}
	c := testClient(t, f)
	s, e := c.Read(t.Context(), Ref{"o/r", 140})
	if e == nil || s.CI != "pending" || strings.Contains(e.Error(), "synthetic-secret") {
		t.Fatalf("%+v %v", s, e)
	}
	args := f.calls()[0]
	for _, want := range []string{"api", "repos/o/r/pulls/140", "--method", "GET", "--include", "--hostname", "github.com"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %s in %v", want, args)
		}
	}
}

func TestHeadChangeRefreshesReviewsAndChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		head := strings.Repeat("a", 40)
		f := &fakeRunner{replies: append(bundle("OPEN", "IN_PROGRESS", "", sha), bundle("OPEN", "COMPLETED", "SUCCESS", head)...)}
		c := testClient(t, f)
		w, _ := c.Watch(Ref{"o/r", 140})
		first, e := w.Next(t.Context(), 0)
		if e != nil {
			t.Fatal(e)
		}
		time.Sleep(7 * time.Second)
		synctest.Wait()
		s, e := w.Next(t.Context(), first.Revision)
		if e != nil || s.Head != head || s.CI != "success" || len(f.calls()) != 8 {
			t.Fatalf("new head %+v %v", s, e)
		}
		for _, args := range f.calls()[6:] {
			for _, arg := range args {
				if strings.HasPrefix(arg, "repos/") && !strings.Contains(arg, head) {
					t.Fatalf("old head checks: %v", args)
				}
			}
		}
	})
}
func TestRateLimitClassesAndMalformedEvidence(t *testing.T) {
	cases := []struct {
		name  string
		reply fakeResponse
		cause string
	}{
		{"primary", fakeResponse{403, "X-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: 4102444800\r\n", `{}`}, "rate_limited"},
		{"secondary text", fakeResponse{403, "", `{"message":"secondary rate limit"}`}, "rate_limited"},
		{"429", fakeResponse{429, "", `{}`}, "rate_limited"},
		{"permission", fakeResponse{403, "", `{"message":"denied"}`}, "permission_denied"},
		{"missing", fakeResponse{404, "", `{}`}, "not_found"},
		{"invalid JSON", fakeResponse{200, "", `{`}, "invalid_response"},
		{"uncached", fakeResponse{304, "", ``}, "uncached_not_modified"},
		{"missing identity", fakeResponse{200, "", `{}`}, "invalid_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{replies: []fakeResponse{tc.reply}}
			c := testClient(t, f)
			s, e := c.Read(t.Context(), Ref{"o/r", 140})
			if e == nil || s.CI != "pending" || s.Done || s.Cause != tc.cause {
				t.Fatalf("%+v %v", s, e)
			}
		})
	}
}
func TestCheckAndReviewPagination(t *testing.T) {
	replies := bundle("OPEN", "COMPLETED", "SUCCESS", sha)
	var first map[string]any
	if err := json.Unmarshal([]byte(replies[1].body), &first); err != nil {
		t.Fatal(err)
	}
	pr := first["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)
	threads := pr["reviewThreads"].(map[string]any)
	threads["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "thread-page"}
	comments := threads["nodes"].([]any)[0].(map[string]any)["comments"].(map[string]any)
	comments["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "comment-page"}
	data, _ := json.Marshal(first)
	replies[1].body = string(data)
	tail := slices.Clone(replies[2:])
	replies = replies[:2]
	replies = append(replies, fakeResponse{200, "", `{"data":{"node":{"comments":{"nodes":[{"id":"C2","body":"second comment"}],"pageInfo":{"hasNextPage":false}}}}}`}, fakeResponse{200, "", `{"data":{"repository":{"pullRequest":{"headRefOid":` + quote(sha) + `,"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`})
	tail[0].body = `{"total_count":2,"check_runs":[{"name":"first","status":"completed","conclusion":"success"}]}`
	replies = append(replies, tail[0], fakeResponse{200, "", `{"total_count":2,"check_runs":[{"name":"second","status":"completed","conclusion":"failure"}]}`}, tail[1])
	f := &fakeRunner{replies: replies}
	c := testClient(t, f)
	s, e := c.Read(t.Context(), Ref{"o/r", 140})
	if e != nil || s.CI != "failure" || len(s.Reviews) != 1 || len(s.Reviews[0].Comments) != 2 || len(s.Checks) != 2 {
		t.Fatalf("pagination %+v %v", s, e)
	}
	if !strings.Contains(strings.Join(f.calls()[2], " "), `after:"comment-page"`) || !strings.Contains(strings.Join(f.calls()[3], " "), `after:"thread-page"`) {
		t.Fatal("pagination cursors not used")
	}
}
func TestPendingOverridesFailuresAndUnknownConclusion(t *testing.T) {
	for _, check := range []string{`{"name":"future","status":"completed","conclusion":"new_state"}`, `{"name":"pending","status":"queued","conclusion":null}`} {
		replies := bundle("OPEN", "COMPLETED", "FAILURE", sha)
		replies[2].body = `{"total_count":2,"check_runs":[{"name":"failed","status":"completed","conclusion":"failure"},` + check + `]}`
		f := &fakeRunner{replies: replies}
		c := testClient(t, f)
		s, e := c.Read(t.Context(), Ref{"o/r", 140})
		if e != nil || s.CI != "pending" || s.Done {
			t.Fatalf("unfinished evidence: %+v %v", s, e)
		}
	}
}
func TestWatcherCloseAndCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeRunner{replies: bundle("OPEN", "IN_PROGRESS", "", sha)}
		c := testClient(t, f)
		w, _ := c.Watch(Ref{"o/r", 140})
		s, e := w.Next(t.Context(), 0)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, e = w.Next(ctx, s.Revision); e != context.Canceled {
			t.Fatal(e)
		}
		if other, e := c.Watch(Ref{"o/r", 140}); e != nil || other != w {
			t.Fatal("one caller stopped shared watch")
		}
		if e = c.Close(t.Context()); e != nil {
			t.Fatal(e)
		}
		s, e = w.Next(t.Context(), s.Revision)
		if e != nil || s.Cause != "cancelled" {
			t.Fatalf("cancelled %+v %v", s, e)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(f.calls()) != 4 {
			t.Fatal("watch survived shutdown")
		}
		if _, e = c.Watch(Ref{"o/r", 140}); e != proc.ErrClosed {
			t.Fatal(e)
		}
	})
}

func TestCompletedWatchIsReadableWithoutAnotherGitHubRead(t *testing.T) {
	f := &fakeRunner{replies: bundle("OPEN", "COMPLETED", "SUCCESS", sha)}
	c := testClient(t, f)
	w, e := c.Watch(Ref{"o/r", 140})
	if e != nil {
		t.Fatal(e)
	}
	s, e := w.Next(t.Context(), 0)
	if e != nil || !s.Done {
		t.Fatal(s, e)
	}
	// Join the worker without closing the client; this proves completion, not a delay.
	c.wg.Wait()
	again, e := c.Watch(Ref{"o/r", 140})
	if e != nil || again != w {
		t.Fatal("terminal snapshot was discarded", e)
	}
	s, e = again.Next(t.Context(), 0)
	if e != nil || s.CI != "success" || len(f.calls()) != 4 {
		t.Fatalf("terminal view: %+v %v", s, e)
	}
}

func TestSnapshotFitsProtocolAndDoesNotClaimPartialEvidence(t *testing.T) {
	replies := bundle("OPEN", "COMPLETED", "SUCCESS", sha)
	replies[1].body = strings.Replace(replies[1].body, "review data", strings.Repeat("x", 600<<10), 1)
	f := &fakeRunner{replies: replies}
	c := testClient(t, f)
	s, e := c.Read(t.Context(), Ref{"o/r", 140})
	if e == nil || s.CI != "pending" || s.Done || s.Cause != "response_too_large" || len(s.Reviews) != 0 {
		t.Fatalf("oversized observation: ci=%s cause=%s error=%v", s.CI, s.Cause, e)
	}
}
