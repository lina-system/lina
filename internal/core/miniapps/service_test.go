// SPDX-License-Identifier: Apache-2.0
package miniapps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/result"
)

type fixture struct {
	svc    *Service
	host   *Host
	root   string
	reads  *fakeReads
	fx     *fakeEffects
	fetch  *fakeFetcher
	checks int
	mu     sync.Mutex
}

type fakeReads struct {
	calls []string
}

func (f *fakeReads) Read(_ context.Context, person, agent, name string, arguments json.RawMessage) (json.RawMessage, error) {
	f.calls = append(f.calls, person+"|"+agent+"|"+name+"|"+string(arguments))
	switch name {
	case "todos":
		return json.RawMessage(`{"rows":[{"ref":"t1","title":"Write the report","state":"open"}]}`), nil
	default:
		return nil, ErrForbidden
	}
}

type fakeEffects struct {
	mu       sync.Mutex
	requests []EffectRequest
	state    string
}

func (f *fakeEffects) Run(_ context.Context, r EffectRequest) (EffectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if f.state == "" {
		return EffectResult{State: "applied", Summary: r.Name + " applied"}, nil
	}
	return EffectResult{State: f.state}, nil
}

type fakeFetcher struct{ hits int }

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string) ([]byte, string, error) {
	f.hits++
	return []byte(`{"price":1}`), "application/json", nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	f := &fixture{root: root, reads: &fakeReads{}, fx: &fakeEffects{}, fetch: &fakeFetcher{}}
	svc, err := New(Config{DB: db, Root: root, Now: time.Now, Reads: f.reads, Effects: f.fx, Fetcher: f.fetch,
		Check: func(context.Context, map[string][]byte, Manifest, []result.MockItem) (CheckResult, error) {
			f.mu.Lock()
			f.checks++
			f.mu.Unlock()
			return CheckResult{Passed: true}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	host, err := NewHost(svc, HostConfig{Browser: Browser{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	f.host = host
	svc.cfg.OnRemove = host.CloseApp
	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

var todoChecklist = []result.MockItem{
	{ID: "title", Description: "Open to-dos", Action: "none"},
	{ID: "done", Description: "Done", Action: "toggle", Target: "row"},
}

func appFiles(extra string) map[string][]byte {
	return map[string][]byte{
		"index.html": []byte(`<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="tokens.css"><link rel="stylesheet" href="base.css"></head><body><main id="app"><h1 data-check="title">Open to-dos</h1><section id="row" class="card"><button data-check="done" data-action="toggle" data-target="row">Done</button></section>` + extra + `</main><script src="app.js"></script></body></html>`),
		"app.js": []byte(`// SPDX-License-Identifier: Apache-2.0
document.getElementById('app').dataset.ready=String(true);
`),
	}
}

func baseManifest() Manifest {
	return Manifest{Name: "Open to-dos", Reads: []string{"todos"}, Requests: []string{"todo.done"}}
}

func create(t *testing.T, f *fixture) Change {
	t.Helper()
	change, err := f.svc.Create(t.Context(), "owner", "agent-1", CreateRequest{Name: "Open to-dos", Request: "Show open to-dos and let me finish one", Checklist: todoChecklist, Manifest: baseManifest(), Files: appFiles(""), StoreShape: []string{"seen"}})
	if err != nil {
		t.Fatal(err)
	}
	return change
}

func TestRegistryRequiresAllowAndReasksOnWidening(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if change.App.Allowed() {
		t.Fatal("a new app opened before the person allowed it")
	}
	view, err := f.host.Open(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Allowed || view.URL != "" || view.Manifest.Name != "Open to-dos" || len(view.Manifest.Reads) != 1 {
		t.Fatalf("first open should show the manifest only: %+v", view)
	}
	allowed, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Allowed() {
		t.Fatal("allow did not permit the current revision")
	}
	if _, err := f.svc.Allow(t.Context(), "other", change.App.ID, change.App.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another person allowed the app: %v", err)
	}

	widened := baseManifest()
	widened.Hosts = []string{"api.example.com"}
	next, err := f.svc.Change(t.Context(), "owner", change.App.ID, ChangeRequest{Request: "Add prices", Checklist: todoChecklist, Manifest: widened, Files: appFiles(`<p class="muted">prices</p>`), StoreShape: []string{"seen"}})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Widened {
		t.Fatal("widening a host did not ask again")
	}
	if next.App.Allowed() {
		t.Fatal("a widened app stayed allowed")
	}
	view, err = f.host.Open(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.URL != "" {
		t.Fatal("a widened app opened without a new allow")
	}
}

func TestReadsAreLimitedToDeclaredNames(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	token := f.token(t, change.App.ID)
	out, err := f.svc.Read(t.Context(), change.App.ID, token, "todos", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(out), "t1") {
		t.Fatalf("declared read failed: %s %v", out, err)
	}
	if _, err := f.svc.Read(t.Context(), change.App.ID, token, "memories", json.RawMessage(`{}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("undeclared read allowed: %v", err)
	}
	for _, call := range f.reads.calls {
		if strings.Contains(call, "memories") {
			t.Fatal("an undeclared read reached Core")
		}
	}
	events, err := f.svc.Events(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || !strings.Contains(events[0], "refused memories") {
		t.Fatalf("refused read was not recorded: %+v", events)
	}
}

func TestTokenIsNotExposedAndAnotherAppsTokenIsRefused(t *testing.T) {
	f := newFixture(t)
	first := create(t, f)
	second, err := f.svc.Create(t.Context(), "owner", "agent-1", CreateRequest{Name: "Second", Request: "Another page", Checklist: todoChecklist, Manifest: baseManifest(), Files: appFiles(""), StoreShape: nil})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.App.ID, second.App.ID} {
		if _, err := f.svc.Allow(t.Context(), "owner", id, 1); err != nil {
			t.Fatal(err)
		}
	}
	if first.App.Cookie() == second.App.Cookie() {
		t.Fatal("two apps share one cookie name on one host")
	}
	a := f.token(t, first.App.ID)
	b := f.token(t, second.App.ID)
	if _, err := f.svc.Authorize(t.Context(), first.App.ID, b); !errors.Is(err, ErrForbidden) {
		t.Fatalf("first app accepted the second app's token: %v", err)
	}
	if _, err := f.svc.Authorize(t.Context(), second.App.ID, a); !errors.Is(err, ErrForbidden) {
		t.Fatalf("second app accepted the first app's token: %v", err)
	}
	view, err := f.host.Open(t.Context(), "owner", first.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view.URL, a) || strings.Contains(view.URL, b) {
		t.Fatal("the frame address carried a token")
	}
}

func (f *fixture) token(t *testing.T, id string) string {
	t.Helper()
	token, err := f.svc.MintToken(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestHostServesEachAppOnItsOwnOriginAndRefusesOtherCookies(t *testing.T) {
	f := newFixture(t)
	first := create(t, f)
	second, err := f.svc.Create(t.Context(), "owner", "agent-1", CreateRequest{Name: "Second", Request: "Another page", Checklist: todoChecklist, Manifest: baseManifest(), Files: appFiles(""), StoreShape: nil})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.App.ID, second.App.ID} {
		if _, err := f.svc.Allow(t.Context(), "owner", id, 1); err != nil {
			t.Fatal(err)
		}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	// openFrame loads the frame page once and returns its app address.
	openFrame := func(id string) (string, *http.Response) {
		view, err := f.host.Open(t.Context(), "owner", id)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(view.URL)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("frame status %d", resp.StatusCode)
		}
		for _, c := range resp.Cookies() {
			if c.Name == cookieName(id) {
				t.Fatal("the frame exposed the app's cookie")
			}
		}
		match := regexp.MustCompile(`src="(http://127\.0\.0\.1:[0-9]+/\?code=[0-9a-f]+)"`).FindStringSubmatch(readAll(t, resp))
		if match == nil {
			t.Fatal("frame carried no app address")
		}
		return match[1], resp
	}
	firstURL, _ := openFrame(first.App.ID)
	origin := originOfURL(t, firstURL)
	appURL := regexp.MustCompile(`src="(http://127\.0\.0\.1:[0-9]+/\?code=[0-9a-f]+)"`).FindStringSubmatch("")
	_ = appURL
	resp, err := client.Get(firstURL)
	if err != nil {
		t.Fatal(err)
	}
	appOrigin := originOfURL(t, firstURL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("app page status %d", resp.StatusCode)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "Open to-dos") {
		t.Fatalf("app page missing content: %s", body)
	}
	if strings.Contains(body, "lina_app_") {
		t.Fatal("the page carried a token")
	}
	if resp.Header.Get("Content-Security-Policy") == "" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatalf("missing strict policy: %q", resp.Header.Get("Content-Security-Policy"))
	}

	// Opening the second app must not replace the first app's cookie.
	secondAppURL, _ := openFrame(second.App.ID)
	resp, err = client.Get(secondAppURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	kept := false
	for _, cookie := range jar.Cookies(mustURL(t, origin)) {
		kept = kept || cookie.Name == cookieName(first.App.ID)
	}
	if !kept {
		t.Fatal("opening another app dropped the first app's cookie")
	}
	// Each gateway refuses a request that carries only another app's cookie.
	foreign := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, appOrigin+"/__lina/read?name=todos", nil)
	req.AddCookie(&http.Cookie{Name: cookieName(second.App.ID), Value: "not-even-a-token"})
	resp, err = foreign.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("gateway accepted another app's cookie: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, appOrigin+"/__lina/read?name=todos", nil)
	resp, err = foreign.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("gateway served without a token: %d", resp.StatusCode)
	}
}

func originOfURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Scheme + "://" + u.Host
}

func originOf(t *testing.T, frameURL string) string {
	t.Helper()
	parts := strings.SplitN(frameURL, "/frame/", 2)
	if len(parts) != 2 {
		t.Fatalf("bad frame url %q", frameURL)
	}
	return parts[0]
}
func codeOf(t *testing.T, frameURL string) string {
	t.Helper()
	_, after, ok := strings.Cut(frameURL, "?code=")
	if !ok {
		t.Fatalf("frame url carries no code: %q", frameURL)
	}
	return after
}
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUndeclaredFetchIsRefused(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	token := f.token(t, change.App.ID)
	if _, _, err := f.svc.Fetch(t.Context(), change.App.ID, token, "https://evil.example/x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("undeclared host fetched: %v", err)
	}
	if f.fetch.hits != 0 {
		t.Fatal("an undeclared host reached the fetcher")
	}
	declared := baseManifest()
	declared.Hosts = []string{"api.example.com"}
	next, err := f.svc.Change(t.Context(), "owner", change.App.ID, ChangeRequest{Request: "fetch prices", Manifest: declared, Checklist: todoChecklist, Files: appFiles(`<p class="muted">prices</p>`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, next.App.Revision); err != nil {
		t.Fatal(err)
	}
	token = f.token(t, change.App.ID)
	if _, _, err := f.svc.Fetch(t.Context(), change.App.ID, token, "http://api.example.com/price"); err != nil {
		t.Fatalf("declared host refused: %v", err)
	}
	if f.fetch.hits != 1 {
		t.Fatalf("declared fetch did not reach the fetcher: %d", f.fetch.hits)
	}
}

func TestWritesRunAsOwnerAgentEffectsAndNeverAnswerApprovals(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	token := f.token(t, change.App.ID)
	out, err := f.svc.Request(t.Context(), change.App.ID, token, "req-1", "todo.done", json.RawMessage(`{"id":"t1"}`))
	if err != nil || out.State != "applied" {
		t.Fatalf("declared request failed: %+v %v", out, err)
	}
	if len(f.fx.requests) != 1 {
		t.Fatalf("request did not reach the effect runner: %+v", f.fx.requests)
	}
	req := f.fx.requests[0]
	if req.Person != "owner" || req.Agent != "agent-1" || req.AppID != change.App.ID || req.AppName != "Open to-dos" || req.RequestID != "req-1" {
		t.Fatalf("effect lost its owner agent or app identity: %+v", req)
	}
	f.fx.state = "waiting"
	out, err = f.svc.Request(t.Context(), change.App.ID, token, "req-2", "todo.done", json.RawMessage(`{"id":"t2"}`))
	if err != nil || out.State != "waiting" {
		t.Fatalf("a waiting effect was not reported: %+v %v", out, err)
	}
	if _, err := f.svc.Request(t.Context(), change.App.ID, token, "req-3", "records.delete", json.RawMessage(`{}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("undeclared request ran: %v", err)
	}
	if len(f.fx.requests) != 2 {
		t.Fatal("an undeclared request reached the effect runner")
	}
}

func TestNoOpRecordsNothingAndUndoRestoresThePreviousRevision(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	noop, err := f.svc.Change(t.Context(), "owner", change.App.ID, ChangeRequest{Request: "make it blue", Manifest: baseManifest(), Checklist: todoChecklist, Files: appFiles("")})
	if err != nil {
		t.Fatal(err)
	}
	if noop.Changed {
		t.Fatal("a change with identical files recorded a revision")
	}
	history, err := f.svc.History(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("no-op recorded a revision: %+v", history)
	}

	next, err := f.svc.Change(t.Context(), "owner", change.App.ID, ChangeRequest{Request: "add a today filter", Manifest: baseManifest(), Checklist: append(todoChecklist, result.MockItem{ID: "today", Description: "Today", Action: "toggle", Target: "row"}), Files: appFiles(`<button data-check="today" data-action="toggle" data-target="row">Today</button>`)})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Changed || next.Revision.Summary == "" || next.App.Revision != 2 {
		t.Fatalf("a real change recorded no diff: %+v", next)
	}
	if !strings.Contains(next.Revision.Summary, "index.html") {
		t.Fatalf("diff does not name the file: %q", next.Revision.Summary)
	}
	if !next.App.Allowed() {
		t.Fatal("a change that keeps the declarations closed the app")
	}
	undone, err := f.svc.Undo(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if undone.App.Revision != 3 {
		t.Fatalf("undo did not make a new revision: %+v", undone.App)
	}
	files, err := f.svc.readRevision(change.App.ID, undone.App.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files["index.html"]), `data-check="today"`) {
		t.Fatal("undo did not restore the previous files")
	}
	restored, err := f.svc.Restore(t.Context(), "owner", change.App.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if restored.App.Revision != 4 {
		t.Fatalf("restore was not a new revision: %d", restored.App.Revision)
	}
	files, err = f.svc.readRevision(change.App.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files["index.html"]), `data-check="today"`) {
		t.Fatal("restore did not bring back revision 2")
	}
}

func TestPreLiveChecksGateARevision(t *testing.T) {
	f := newFixture(t)
	rejected := 0
	f.svc.cfg.Check = func(_ context.Context, files map[string][]byte, _ Manifest, _ []result.MockItem) (CheckResult, error) {
		if strings.Contains(string(files["index.html"]), "raw") {
			rejected++
			return CheckResult{Passed: false, Checks: []result.MockCheck{{ID: "tokens", Passed: false}}, Reason: "tokens"}, nil
		}
		return CheckResult{Passed: true}, nil
	}
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Change(t.Context(), "owner", change.App.ID, ChangeRequest{Request: "red header", Manifest: baseManifest(), Checklist: todoChecklist, Files: appFiles(`<p class="raw">x</p>`)})
	if !errors.Is(err, ErrCheck) {
		t.Fatalf("a failing check was accepted: %v", err)
	}
	if rejected != 1 {
		t.Fatalf("the check did not run: %d", rejected)
	}
	if _, err := f.svc.Change(t.Context(), "owner", change.App.ID, ChangeRequest{Request: "too many", Manifest: baseManifest(), Checklist: todoChecklist, Files: map[string][]byte{"index.html": []byte(strings.Repeat("x", MaxFileBytes+1)), "app.js": []byte("()=>0")}}); err == nil {
		t.Fatal("an oversized file passed the size limit")
	}
	checks, err := Static(map[string][]byte{"index.html": []byte(`<script src="https://cdn.example/x.js"></script>`), "app.js": []byte("()=>0")}, todoChecklist)
	if err != nil {
		t.Fatal(err)
	}
	network := false
	elements := false
	for _, check := range checks {
		if check.ID == "network" && !check.Passed {
			network = true
		}
		if check.ID == "elements" {
			elements = !check.Passed
		}
	}
	if !network || !elements {
		t.Fatalf("static checks missed a remote address or a missing item: %+v", checks)
	}
}

func TestRemoveRevokesAndMovesFilesToTrash(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	token := f.token(t, change.App.ID)
	closed := false
	f.svc.cfg.OnRemove = func(string) { closed = true }
	if err := f.svc.Remove(t.Context(), "owner", change.App.ID); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("remove did not close the live origin")
	}
	if _, err := f.svc.Authorize(t.Context(), change.App.ID, token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed app still accepted its token: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "miniapps", change.App.ID)); !os.IsNotExist(err) {
		t.Fatal("app files stayed in place")
	}
	entries, err := os.ReadDir(filepath.Join(f.root, "trash", "miniapps"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("app files did not move to the trash: %v %v", entries, err)
	}
}

func TestManifestRejectsUnboundedDeclarations(t *testing.T) {
	for name, m := range map[string]Manifest{
		"empty":     {Name: ""},
		"bad read":  {Name: "x", Reads: []string{"../etc"}},
		"bad host":  {Name: "x", Hosts: []string{"admin@example.com"}},
		"path host": {Name: "x", Hosts: []string{"example.com/x"}},
		"schedule":  {Name: "x", Schedule: "every minute"},
	} {
		if ValidateManifest(m) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if ValidateManifest(Manifest{Name: "x", Reads: []string{"todos", "running"}, Hosts: []string{"api.example.com"}, Schedule: "15m"}) != nil {
		t.Fatal("a valid manifest was refused")
	}
}
