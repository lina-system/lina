// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func miniAppBrowser() bool {
	if p := os.Getenv("LINA_CHROMIUM"); p != "" {
		_, err := os.Stat(p)
		return err == nil
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if _, err := os.Stat("/usr/bin/" + name); err == nil {
			return true
		}
	}
	return false
}

// miniAppHTML is the app the worker writes: a title and one control that
// finishes a to-do. The template supplies the styles, fonts and client.
func miniAppHTML(extra string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="tokens.css"><link rel="stylesheet" href="base.css"></head><body><main id="app"><h1 data-check="title">Open to-dos</h1><section id="row" class="card"><button data-check="done" data-action="toggle" data-target="row">Done</button></section>` + extra + `</main><script src="app.js"></script></body></html>`
}

func miniAppFiles(extra string) map[string]string {
	return map[string]string{
		"index.html": miniAppHTML(extra),
		"app.js":     "// SPDX-License-Identifier: Apache-2.0\ndocument.getElementById('app').dataset.ready=String(true);\n",
	}
}

func miniAppChecklist() []any {
	return []any{
		map[string]any{"id": "title", "description": "Open to-dos", "action": "none"},
		map[string]any{"id": "done", "description": "Done", "action": "toggle", "target": "row"},
	}
}

// TestMiniAppsThroughRunningCore builds an app on request, changes it by
// talking, serves it under its own origin and undoes the change.
func TestMiniAppsThroughRunningCore(t *testing.T) {
	if !miniAppBrowser() {
		t.Skip("no installed Chromium")
	}
	var mu sync.Mutex
	appID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := decodeRequest(t, r)
		item := last(q)
		if item.Type == "function_call_output" {
			reply(w, "Done.")
			return
		}
		switch {
		case strings.Contains(item.Content, "open to-dos"):
			toolCall(w, "create", "mini_app", map[string]any{
				"op": "create", "name": "Open to-dos",
				"request": "a page that shows open to-dos and lets me finish one",
				"manifest": map[string]any{"name": "Open to-dos", "reads": []string{"todos"}, "requests": []string{"todo.done"}},
				"checklist": miniAppChecklist(),
				"files":     miniAppFiles(""),
			})
		case strings.Contains(item.Content, "오늘만"):
			mu.Lock()
			id := appID
			mu.Unlock()
			toolCall(w, "change", "mini_app", map[string]any{
				"op": "change", "app": id,
				"request":   "위에 '오늘만' 필터 넣어 줘",
				"manifest":  map[string]any{"name": "Open to-dos", "reads": []string{"todos", "running"}, "requests": []string{"todo.done"}},
				"checklist": miniAppChecklist(),
				"files":     miniAppFiles(`<p id="filter">오늘만</p>`),
			})
		default:
			t.Errorf("unexpected input: %s", item.Content)
			reply(w, "Unexpected input.")
		}
	}))
	t.Cleanup(server.Close)
	h := newCore(t, server.URL)
	lina := h.agent("lina")
	h.turn(lina, "app-create", "Build me a page that shows open to-dos and lets me finish one")
	id, revision, allowed := miniAppRow(t, h)
	if revision != 1 || allowed != 0 {
		t.Fatalf("first revision was not kept for the person: revision=%d allowed=%d", revision, allowed)
	}
	first := miniAppFile(t, h, id, 1, "index.html")
	if !strings.Contains(first, `data-check="done"`) {
		t.Fatalf("served files lost the control: %s", first)
	}
	mu.Lock()
	appID = id
	mu.Unlock()

	// The person allows the first revision and opens the app. The frame's
	// one-time code is exchanged for the app's own cookie.
	c := miniAppClient(t, h)
	if got := miniAppCommand(t, c, protocol.MiniAppRequest{Operation: "allow", App: &id, Revision: revisionNumber(1)}); got.Code != nil {
		t.Fatalf("allow refused: %s", *got.Code)
	}
	opened := miniAppCommand(t, c, protocol.MiniAppRequest{Operation: "open", App: &id})
	if opened.URL == nil {
		t.Fatalf("an allowed app did not open: %+v", opened)
	}
	frame := httpGet(t, *opened.URL, nil)
	if !strings.Contains(frame.body, `id="app"`) {
		t.Fatalf("frame page is not the mini app frame: %s", frame.body)
	}
	src := between(frame.body, `src="`, `"`)
	if !strings.Contains(src, "/?code=") {
		t.Fatalf("frame carried no one-time code: %s", src)
	}
	app := httpGet(t, src, nil)
	if app.status != http.StatusOK || !strings.Contains(app.body, "Open to-dos") {
		t.Fatalf("app origin did not serve the app: %d %s", app.status, app.body)
	}
	if !strings.Contains(app.cookie, "lina_app_"+id) {
		t.Fatalf("app origin did not set its own cookie: %q", app.cookie)
	}

	// The person asks for a change. A widening re-asks before it goes live.
	h.turn(lina, "app-change", "위에 '오늘만' 필터 넣어 줘")
	_, revision, allowed = miniAppRow(t, h)
	if revision != 2 || allowed != 1 {
		t.Fatalf("a widened revision did not wait for the person: revision=%d allowed=%d", revision, allowed)
	}
	second := miniAppFile(t, h, id, 2, "index.html")
	if !strings.Contains(second, "오늘만") || second == first {
		t.Fatalf("the change did not reach the stored files: %s", second)
	}
	closed := miniAppCommand(t, c, protocol.MiniAppRequest{Operation: "open", App: &id})
	if closed.URL != nil || closed.App == nil || closed.App.Allowed {
		t.Fatalf("an unallowed revision opened: %+v", closed)
	}
	if got := miniAppCommand(t, c, protocol.MiniAppRequest{Operation: "allow", App: &id, Revision: revisionNumber(2)}); got.Code != nil {
		t.Fatalf("allow refused the second revision: %s", *got.Code)
	}

	// Undo returns to the previous revision as a new one.
	if got := miniAppCommand(t, c, protocol.MiniAppRequest{Operation: "undo", App: &id}); got.Code != nil {
		t.Fatalf("undo refused: %s", *got.Code)
	}
	_, revision, _ = miniAppRow(t, h)
	if revision != 3 {
		t.Fatalf("undo was not recorded as a new revision: %d", revision)
	}
	if restored := miniAppFile(t, h, id, 3, "index.html"); restored != first {
		t.Fatalf("undo did not restore the previous files: %s", restored)
	}
}

func revisionNumber(n int) *json.Number {
	value := json.Number(itoa(n))
	return &value
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func miniAppClient(t *testing.T, h *coreHarness) *clientapi.Client {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "m4", []protocol.Capability{{Name: "mini_apps", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func miniAppCommand(t *testing.T, c *clientapi.Client, p protocol.MiniAppRequest) protocol.MiniAppResult {
	t.Helper()
	id, err := c.MiniApp(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatalf("%s: %+v", p.Operation, frame.Error)
		}
		if frame.ReplyTo == nil || *frame.ReplyTo != id {
			continue
		}
		answer, ok := frame.Payload.(protocol.MiniAppResult)
		if !ok {
			t.Fatalf("%s answered %T", p.Operation, frame.Payload)
		}
		return answer
	}
}

func miniAppRow(t *testing.T, h *coreHarness) (string, int64, int64) {
	t.Helper()
	var id string
	var revision, allowed int64
	h.read(func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, `SELECT id,revision,allowed_revision FROM mini_app WHERE removed=0`).Scan(&id, &revision, &allowed)
	})
	return id, revision, allowed
}

func miniAppFile(t *testing.T, h *coreHarness, id string, revision int, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.root, "miniapps", id, "r", itoa(revision), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type httpResult struct {
	status int
	body   string
	cookie string
}

func httpGet(t *testing.T, url string, cookie *http.Cookie) httpResult {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	out := httpResult{status: response.StatusCode, body: string(body)}
	if cookies := response.Cookies(); len(cookies) > 0 {
		out.cookie = cookies[0].Name + "=" + cookies[0].Value
	}
	return out
}

func between(text, start, end string) string {
	_, tail, ok := strings.Cut(text, start)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(tail, end)
	return value
}
