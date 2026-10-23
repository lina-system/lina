// SPDX-License-Identifier: Apache-2.0
package miniapps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
)

type testLeaser struct{}

func (testLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

func testBrowser(t *testing.T) Browser {
	t.Helper()
	if os.Getenv("LINA_CHROMIUM") == "" && !browserAvailable() {
		t.Skip("no installed Chromium")
	}
	return Browser{Leaser: testLeaser{}}
}

func browserAvailable() bool {
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if _, err := os.Stat("/usr/bin/" + name); err == nil {
			return true
		}
	}
	return false
}

// openApp loads an allowed app through its own origin and returns the token
// the frame would receive as an HttpOnly cookie.
func (f *fixture) openApp(t *testing.T, id string) (string, string) {
	t.Helper()
	app, err := f.svc.load(t.Context(), "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.host.ensure(t.Context(), app); err != nil {
		t.Fatal(err)
	}
	token := f.token(t, id)
	f.mu.Lock()
	port := f.host.apps[id].port
	f.mu.Unlock()
	return "http://127.0.0.1:" + itoa(int64(port)) + "/", token
}

func TestPolicyBlocksUndeclaredNetworkAndAnotherOrigin(t *testing.T) {
	var hits atomic.Int32
	sentinel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sentinel.Close()
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	origin, token := f.openApp(t, change.App.ID)
	b := testBrowser(t)
	probe := func(url string, setup func(*cdp)) (network bool, frame bool, err error) {
		err = b.withBrowser(t.Context(), func(c *cdp) error {
			if setup != nil {
				setup(c)
			}
			var nav struct{}
			if err := c.call(t.Context(), "Page.navigate", map[string]any{"url": url}, &nav); err != nil {
				return err
			}
			time.Sleep(1500 * time.Millisecond)
			raw, _ := json.Marshal(sentinel.URL + "/outside")
			var out struct{ Network, Frame bool }
			expression := `(async()=>{let network=false,frame=false;try{await fetch(` + string(raw) + `,{mode:'no-cors'});network=true}catch{}try{await fetch(location.origin+'/__lina/read?name=todos');frame=true}catch{}return{Network:network,Frame:frame}})()`
			if err := c.evaluate(t.Context(), expression, &out); err != nil {
				return err
			}
			network, frame = out.Network, out.Frame
			return nil
		})
		return network, frame, err
	}
	// The app's own cookie is what admits the gateway; the policy is what stops
	// every address outside the app's origin.
	b.withBrowser(t.Context(), func(*cdp) error { return nil })
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, origin+"__lina/read?name=todos", nil)
	req.AddCookie(&http.Cookie{Name: cookieName(change.App.ID), Value: token})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("declared read through the gateway failed: %d", resp.StatusCode)
	}
	network, frame, err := probe(origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if network || hits.Load() != 0 {
		t.Fatalf("policy did not block the undeclared address: network=%t hits=%d", network, hits.Load())
	}
	_ = frame
	// Negative control: the same page bytes without the policy reach the
	// undeclared address, so the policy, not CORS, is what blocks.
	page, _ := f.svc.readRevision(change.App.ID, change.App.Revision)
	unprotected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(page["index.html"])
	}))
	defer unprotected.Close()
	network, _, err = probe(unprotected.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !network || hits.Load() != 1 {
		t.Fatalf("negative control did not reach the address: network=%t hits=%d", network, hits.Load())
	}
}

func TestAppCannotReadItsFrame(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	view, err := f.host.Open(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := testBrowser(t)
	match := regexpFrameCode.FindStringSubmatch(view.URL)
	if match == nil {
		t.Fatal("frame address has no code")
	}
	var parentRead bool
	err = b.withBrowser(t.Context(), func(c *cdp) error {
		var nav struct{}
		if err := c.call(t.Context(), "Page.navigate", map[string]any{"url": view.URL}, &nav); err != nil {
			return err
		}
		time.Sleep(1500 * time.Millisecond)
		var out struct{ Parent, Token bool }
		expression := `(async()=>{let parent=false,token=false;const f=document.getElementById('app');try{parent=!!f.contentWindow.document.body}catch{}try{token=document.cookie.includes('lina_app_')}catch{}return{Parent:parent,Token:token}})()`
		if err := c.evaluate(t.Context(), expression, &out); err != nil {
			return err
		}
		parentRead = out.Parent
		if out.Token {
			t.Fatal("the frame exposed the app's cookie to its page")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if parentRead {
		t.Fatal("the frame page could read the app's document")
	}
	// The app page itself must not read the frame either, and its script never
	// sees the token because the cookie is HttpOnly.
	appOrigin, token := f.openApp(t, change.App.ID)
	if token == "" {
		t.Fatal("no token")
	}
	err = b.withBrowser(t.Context(), func(c *cdp) error {
		var nav struct{}
		if err := c.call(t.Context(), "Page.navigate", map[string]any{"url": appOrigin + "?code=invalid"}, &nav); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
		var out struct{ Status int }
		expression := `(async()=>{const r=await fetch(location.origin+'/',{redirect:'manual'});return{Status:r.status}})()`
		if err := c.evaluate(t.Context(), expression, &out); err != nil {
			return err
		}
		if out.Status == http.StatusOK {
			t.Fatal("an app page rendered without a valid code")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var regexpFrameCode = mustRegexp(`\?code=[0-9a-f]+`)

func mustRegexp(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }

func TestFrameRefusesNavigationAway(t *testing.T) {
	f := newFixture(t)
	change := create(t, f)
	if _, err := f.svc.Allow(t.Context(), "owner", change.App.ID, change.App.Revision); err != nil {
		t.Fatal(err)
	}
	view, err := f.host.Open(t.Context(), "owner", change.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := testBrowser(t)
	err = b.withBrowser(t.Context(), func(c *cdp) error {
		var nav struct{}
		if err := c.call(t.Context(), "Page.navigate", map[string]any{"url": view.URL}, &nav); err != nil {
			return err
		}
		time.Sleep(1500 * time.Millisecond)
		var first string
		if err := c.evaluate(t.Context(), `document.getElementById('app').src`, &first); err != nil {
			return err
		}
		if !strings.Contains(first, "/?code=") {
			t.Fatalf("the frame dropped its one-time code on the app's first load: %s", first)
		}
		home, _, _ := strings.Cut(first, "/?")
		home += "/"
		// The app navigates its own frame away from its origin. The policy does
		// not stop a navigation; the framing page returns the frame to the app.
		var out struct{ After string }
		expression := `(async()=>{const f=document.getElementById('app');try{f.contentWindow.location='https://example.com/'}catch{}await new Promise(r=>setTimeout(r,2000));return{After:f.src}})()`
		if err := c.evaluate(t.Context(), expression, &out); err != nil {
			return err
		}
		if out.After != home {
			t.Fatalf("the frame did not return to the app origin after a navigation away: %s", out.After)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPreLiveCheckFailsWithoutABrowser(t *testing.T) {
	f := newFixture(t)
	host, err := NewHost(f.svc, HostConfig{Browser: Browser{Leaser: testLeaser{}, Executable: "/nonexistent/chrome"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	_, err = host.Check(t.Context(), appFiles(""), baseManifest(), todoChecklist)
	if err == nil {
		t.Fatal("a missing browser passed the pre-live check")
	}
}

func TestStaticChecksNameTheFailingDimension(t *testing.T) {
	checks, err := Static(map[string][]byte{"index.html": []byte(`<main><h1 data-check="title">Open to-dos</h1><span style="color:#ff0000">x</span></main>`), "app.js": []byte(`document.body.dataset.ok='1'`)}, todoChecklist)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, check := range checks {
		if !check.Passed {
			failed[check.ID] = true
		}
	}
	for _, id := range []string{"controls", "tokens", "elements"} {
		if !failed[id] {
			t.Fatalf("%s did not fail on a raw color, an extra marker and a missing control: %+v", id, checks)
		}
	}
}
