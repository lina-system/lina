// SPDX-License-Identifier: Apache-2.0
package miniapps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	generatedui "github.com/thisisjun786/lina/design"
	"github.com/thisisjun786/lina/kit/result"
)

// Host gives every mini app its own loopback origin and, when configured, the
// same port on the tailnet. Nothing is bound to a public address. The frame page
// lives on a separate origin so the app cannot read it.
type Host struct {
	svc   *Service
	cfg   HostConfig
	br    Browser
	mu    sync.Mutex
	apps  map[string]*origin
	code  map[string]codeState
	rate  map[string]*bucket
	frame *frameOrigin

	// test hooks
	listen func(ctx context.Context, network, address string) (net.Listener, error)
}

type HostConfig struct {
	// TailnetIP is a Tailscale address. Empty keeps every listener on loopback.
	TailnetIP string
	// Listen is replaceable in tests; production uses net.ListenConfig.
	Listen  func(ctx context.Context, network, address string) (net.Listener, error)
	Browser Browser
}

type origin struct {
	appID    string
	listener net.Listener
	tailnet  net.Listener
	server   *http.Server
	port     int
	done     chan struct{}
}

type frameOrigin struct {
	listener net.Listener
	tailnet  net.Listener
	server   *http.Server
	port     int
	done     chan struct{}
}

type codeState struct {
	appID   string
	expires time.Time
}

type bucket struct {
	read, request, store, fetch int
	reset                       time.Time
}

// OpenView is what a client shows before or after the person allows an app.
type OpenView struct {
	App      App      `json:"app"`
	Allowed  bool     `json:"allowed"`
	URL      string   `json:"url,omitempty"`
	Tailnet  string   `json:"tailnet,omitempty"`
	Manifest Manifest `json:"manifest"`
}

func NewHost(svc *Service, cfg HostConfig) (*Host, error) {
	if svc == nil {
		return nil, ErrUnavailable
	}
	if cfg.Listen == nil {
		cfg.Listen = (&net.ListenConfig{}).Listen
	}
	return &Host{svc: svc, cfg: cfg, br: cfg.Browser, apps: map[string]*origin{}, code: map[string]codeState{}, rate: map[string]*bucket{}, listen: cfg.Listen}, nil
}

// SetCheck installs the pre-live check that uses this host's preview and
// browser. The service is constructed first so the host can read it.
func (h *Host) SetCheck() {
	h.svc.cfg.Check = h.Check
}

// Start binds the frame origin. App origins bind on first open or allow.
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.frame != nil {
		return nil
	}
	f, err := h.bind(ctx, func(port int) http.Handler { return http.HandlerFunc(h.serveFrame) })
	if err != nil {
		return err
	}
	h.frame = f
	return nil
}

func (h *Host) bind(ctx context.Context, handler func(port int) http.Handler) (*frameOrigin, error) {
	if h.frame != nil {
		return nil, ErrUnavailable
	}
	h.mu.Unlock()
	defer h.mu.Lock()
	l, err := h.listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	f := &frameOrigin{listener: l, port: port, done: make(chan struct{})}
	if h.cfg.TailnetIP != "" {
		if tl, err := h.listen(ctx, "tcp", net.JoinHostPort(h.cfg.TailnetIP, fmt.Sprint(port))); err == nil {
			f.tailnet = tl
		}
	}
	h.serve(f, handler(port))
	if f.tailnet != nil {
		go h.serveListener(f.tailnet, handler(port))
	}
	return f, nil
}

func (h *Host) serve(f *frameOrigin, handler http.Handler) {
	f.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		_ = f.server.Serve(f.listener)
		close(f.done)
	}()
}
func (h *Host) serveListener(l net.Listener, handler http.Handler) {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	_ = server.Serve(l)
}

func (h *Host) stop(f *frameOrigin) {
	if f == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = f.server.Shutdown(ctx)
	if f.tailnet != nil {
		_ = f.tailnet.Close()
	}
	<-f.done
}

// Close stops every origin.
func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, o := range h.apps {
		h.stopOrigin(o)
		delete(h.apps, id)
	}
	if h.frame != nil {
		h.stop(h.frame)
		h.frame = nil
	}
}

func (h *Host) stopOrigin(o *origin) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = o.server.Shutdown(ctx)
	if o.tailnet != nil {
		_ = o.tailnet.Close()
	}
	<-o.done
}

// CloseApp closes one app origin before its files move to the trash.
func (h *Host) CloseApp(appID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if o := h.apps[appID]; o != nil {
		h.stopOrigin(o)
		delete(h.apps, appID)
	}
	delete(h.rate, appID)
}

// Open returns the frame address with a one-time code. Before the person allows
// the current revision it returns the manifest only, never a live origin.
func (h *Host) Open(ctx context.Context, person, appID string) (OpenView, error) {
	app, err := h.svc.load(ctx, person, appID)
	if err != nil {
		return OpenView{}, err
	}
	view := OpenView{App: app, Allowed: app.Allowed(), Manifest: app.Manifest}
	if !view.Allowed {
		return view, nil
	}
	if err := h.ensure(ctx, app); err != nil {
		return OpenView{}, err
	}
	code, err := h.issue(appID)
	if err != nil {
		return OpenView{}, err
	}
	h.mu.Lock()
	f := h.frame
	h.mu.Unlock()
	if f == nil {
		return OpenView{}, ErrUnavailable
	}
	view.URL = fmt.Sprintf("http://127.0.0.1:%d/frame/%s?code=%s", f.port, appID, code)
	if h.cfg.TailnetIP != "" && f.tailnet != nil {
		view.Tailnet = fmt.Sprintf("http://%s/frame/%s?code=%s", net.JoinHostPort(h.cfg.TailnetIP, fmt.Sprint(f.port)), appID, code)
	}
	return view, nil
}

func (h *Host) issue(appID string) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw)
	now := time.Now()
	h.mu.Lock()
	for key, value := range h.code {
		if !value.expires.After(now) {
			delete(h.code, key)
		}
	}
	h.code[code] = codeState{appID: appID, expires: now.Add(60 * time.Second)}
	h.mu.Unlock()
	return code, nil
}

func (h *Host) consume(code, appID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, ok := h.code[code]
	if !ok || state.appID != appID || !state.expires.After(time.Now()) {
		return false
	}
	delete(h.code, code)
	return true
}

// ensure binds an app origin and mints its cookie token when the current one is
// no longer valid (a change, narrowing or restart rotates it).
func (h *Host) ensure(ctx context.Context, app App) error {
	h.mu.Lock()
	o := h.apps[app.ID]
	if o != nil {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	l, err := h.listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	o = &origin{appID: app.ID, listener: l, port: port, done: make(chan struct{})}
	if h.cfg.TailnetIP != "" {
		if tl, err := h.listen(ctx, "tcp", net.JoinHostPort(h.cfg.TailnetIP, fmt.Sprint(port))); err == nil {
			o.tailnet = tl
		}
	}
	o.server = &http.Server{Handler: h.appHandler(o), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		_ = o.server.Serve(o.listener)
		close(o.done)
	}()
	if o.tailnet != nil {
		go h.serveListener(o.tailnet, h.appHandler(o))
	}
	h.mu.Lock()
	if existing := h.apps[app.ID]; existing != nil {
		h.mu.Unlock()
		h.stopOrigin(o)
		return nil
	}
	h.apps[app.ID] = o
	h.mu.Unlock()
	if _, err := h.token(ctx, app.ID); err != nil {
		h.CloseApp(app.ID)
		return err
	}
	return nil
}

func (h *Host) token(ctx context.Context, appID string) (string, error) {
	return h.svc.MintToken(ctx, appID)
}

func (h *Host) appHandler(o *origin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", h.policy())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		if !h.limit(o.appID, r.URL.Path) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		token, ok := h.cookie(w, r, o.appID)
		if !ok {
			return
		}
		switch {
		case r.URL.Path == "/__lina/session":
			http.NotFound(w, r)
		case r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/") && !strings.HasPrefix(r.URL.Path, "/__lina/"):
			if route, ok := h.serveFile(w, r, o.appID, token); !ok {
				http.Error(w, "not found", route)
			}
		case r.URL.Path == "/__lina/read":
			h.serveRead(w, r, o.appID, token)
		case r.URL.Path == "/__lina/request":
			h.serveRequest(w, r, o.appID, token)
		case r.URL.Path == "/__lina/store":
			h.serveStore(w, r, o.appID, token)
		case r.URL.Path == "/__lina/fetch":
			h.serveFetch(w, r, o.appID, token)
		default:
			http.NotFound(w, r)
		}
	})
}

// cookie admits the app's own cookie and exchanges a one-time code. It never
// reads another app's cookie; a request carrying only one is refused.
func (h *Host) cookie(w http.ResponseWriter, r *http.Request, appID string) (string, bool) {
	name := cookieName(appID)
	if r.URL.Path == "/" && r.URL.Query().Get("code") != "" {
		code := r.URL.Query().Get("code")
		if !h.consume(code, appID) {
			http.Error(w, "invalid code", http.StatusForbidden)
			return "", false
		}
		token, err := h.token(r.Context(), appID)
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return "", false
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return "", false
	}
	if c, err := r.Cookie(name); err == nil && c.Value != "" {
		if _, err := h.svc.Authorize(r.Context(), appID, c.Value); err == nil {
			return c.Value, true
		}
	}
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, "lina_app_") {
			http.Error(w, "not this app", http.StatusUnauthorized)
			return "", false
		}
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", false
}

func (h *Host) policy() string {
	h.mu.Lock()
	frame := h.frame
	h.mu.Unlock()
	ancestors := "'none'"
	if frame != nil {
		ancestors = fmt.Sprintf("http://127.0.0.1:%d", frame.port)
		if h.cfg.TailnetIP != "" && frame.tailnet != nil {
			ancestors += fmt.Sprintf(" http://%s", net.JoinHostPort(h.cfg.TailnetIP, fmt.Sprint(frame.port)))
		}
	}
	return "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors " + ancestors + "; base-uri 'none'; form-action 'none'; object-src 'none'"
}

func (h *Host) limit(appID, route string) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.rate[appID]
	if b == nil || !b.reset.After(now) {
		b = &bucket{reset: now.Add(time.Minute)}
		h.rate[appID] = b
	}
	switch {
	case route == "/__lina/read":
		b.read++
		return b.read <= 240
	case route == "/__lina/request":
		b.request++
		return b.request <= 60
	case route == "/__lina/store":
		b.store++
		return b.store <= 120
	case route == "/__lina/fetch":
		b.fetch++
		return b.fetch <= 30
	}
	return true
}

func (h *Host) serveFile(w http.ResponseWriter, r *http.Request, appID, token string) (int, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return http.StatusMethodNotAllowed, false
	}
	if _, err := h.svc.Authorize(r.Context(), appID, token); err != nil {
		return http.StatusUnauthorized, false
	}
	app, err := h.svc.loadAnyPerson(r.Context(), appID)
	if err != nil {
		return http.StatusNotFound, false
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	var data []byte
	// The fixed template is served first: its font and styles are not app files
	// and would otherwise fail the app path rules.
	if template := generatedui.MiniAppFiles(); template[name] != nil && name != "index.html" {
		data = template[name]
	} else {
		if err := validatePath(name); err != nil {
			return http.StatusNotFound, false
		}
		files, err := h.svc.readRevision(app.ID, app.Revision)
		if err != nil {
			return http.StatusNotFound, false
		}
		data = files[name]
	}
	if data == nil {
		return http.StatusNotFound, false
	}
	media := mime.TypeByExtension(path.Ext(name))
	if media == "" {
		media = "application/octet-stream"
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
	return http.StatusOK, true
}

func (h *Host) serveRead(w http.ResponseWriter, r *http.Request, appID, token string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	var arguments json.RawMessage = json.RawMessage(`{}`)
	if r.Method == http.MethodPost {
		var body struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := decodeBody(w, r, &body); err != nil || body.Name == "" {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		name = body.Name
		if len(body.Arguments) > 0 {
			arguments = body.Arguments
		}
	}
	out, err := h.svc.Read(r.Context(), appID, token, name, arguments)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrForbidden) {
			status = http.StatusForbidden
		}
		http.Error(w, "refused", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (h *Host) serveRequest(w http.ResponseWriter, r *http.Request, appID, token string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.Name == "" {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	if len(body.Arguments) == 0 {
		body.Arguments = json.RawMessage(`{}`)
	}
	out, err := h.svc.Request(r.Context(), appID, token, body.ID, body.Name, body.Arguments)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrForbidden) {
			status = http.StatusForbidden
		}
		http.Error(w, "refused", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Host) serveStore(w http.ResponseWriter, r *http.Request, appID, token string) {
	switch r.Method {
	case http.MethodGet:
		value, err := h.svc.Store(r.Context(), appID, token)
		if err != nil {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(value)
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxStoreBytes))
		if err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		if err := h.svc.PutStore(r.Context(), appID, token, body); err != nil {
			http.Error(w, "refused", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (h *Host) serveFetch(w http.ResponseWriter, r *http.Request, appID, token string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeBody(w, r, &body); err != nil || body.URL == "" {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	data, media, err := h.svc.Fetch(r.Context(), appID, token, body.URL)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrForbidden) {
			status = http.StatusForbidden
		}
		http.Error(w, "refused", status)
		return
	}
	w.Header().Set("Content-Type", media)
	_, _ = w.Write(data)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return ErrInvalid
	}
	return json.Unmarshal(raw, dst)
}

// serveFrame renders the surface frame. It is cross-origin from the app, so the
// app cannot read it, and it bounces any in-frame navigation back to the app's
// origin. The sandbox omits top navigation, popups, forms and downloads.
func (h *Host) serveFrame(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/frame/") {
		http.NotFound(w, r)
		return
	}
	appID := strings.TrimPrefix(r.URL.Path, "/frame/")
	if !appIDPattern.MatchString(appID) || !h.consume(r.URL.Query().Get("code"), appID) {
		http.Error(w, "invalid code", http.StatusForbidden)
		return
	}
	h.mu.Lock()
	o := h.apps[appID]
	h.mu.Unlock()
	if o == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	appCode, err := h.issue(appID)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	origin, ok := h.frameOrigin(r)
	if !ok {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	appURL := fmt.Sprintf("http://%s/?code=%s", net.JoinHostPort(origin, fmt.Sprint(o.port)), appCode)
	home := fmt.Sprintf("http://%s/", net.JoinHostPort(origin, fmt.Sprint(o.port)))
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	n := hex.EncodeToString(nonce)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-src http://"+net.JoinHostPort(origin, fmt.Sprint(o.port))+"; style-src 'nonce-"+n+"'; script-src 'nonce-"+n+"'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>LINA mini app</title><style nonce="` + n + `">html,body{margin:0;height:100%;background:transparent}iframe{border:0;width:100%;height:100%;display:block}</style></head><body><iframe id="app" title="Mini app" sandbox="allow-scripts allow-same-origin" referrerpolicy="no-referrer" src="` + appURL + `"></iframe><script nonce="` + n + `">(()=>{const app=document.getElementById('app');const home=` + jsonString(home) + `;const homeOrigin=new URL(home).origin;app.addEventListener('load',()=>{let same=false;try{same=app.contentWindow.location.origin===homeOrigin}catch{same=false}if(!same)app.src=home});})()</script></body></html>`
	_, _ = w.Write([]byte(page))
}

// frameOrigin matches the host the frame was reached on: loopback or the
// configured tailnet address. A frame served to another device must point at
// that device's view of Core, never at its own loopback.
func (h *Host) frameOrigin(r *http.Request) (string, bool) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		return "", false
	}
	h.mu.Lock()
	frame := h.frame
	h.mu.Unlock()
	if frame == nil || port != fmt.Sprint(frame.port) {
		return "", false
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	if host == "127.0.0.1" {
		return host, true
	}
	if h.cfg.TailnetIP != "" && host == h.cfg.TailnetIP {
		return host, true
	}
	return "", false
}

func jsonString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// Preview serves a candidate revision on a temporary loopback origin for the
// pre-live check. It has no token and no gateway; the app must render its
// checklist skeleton before data arrives.
func (h *Host) Preview(ctx context.Context, files map[string][]byte) (string, func(), error) {
	l, err := h.listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	template := generatedui.MiniAppFiles()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		data, ok := files[name]
		if !ok && name != "index.html" {
			data = template[name]
		}
		if data == nil {
			if err := validatePath(name); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
		_, _ = w.Write(data)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(l)
		close(done)
	}()
	closeFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		<-done
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", port), closeFn, nil
}

// Check runs every pre-live dimension. Static failures never start a browser;
// a browser that cannot run fails the revision.
func (h *Host) Check(ctx context.Context, files map[string][]byte, manifest Manifest, checklist []result.MockItem) (CheckResult, error) {
	if err := ValidateManifest(manifest); err != nil {
		return CheckResult{}, err
	}
	if err := ValidateFiles(files); err != nil {
		return CheckResult{}, err
	}
	if result.ValidateMockChecklist(checklist) != nil {
		return CheckResult{}, ErrInvalid
	}
	passed := staticChecks(files, checklist)
	checks := mergeChecks(nil, passed)
	if !allPassedExceptLayout(checks) {
		return CheckResult{Passed: false, Checks: checks, Reason: firstFailure(checks)}, nil
	}
	url, closeFn, err := h.Preview(ctx, files)
	if err != nil {
		return CheckResult{}, err
	}
	defer closeFn()
	extra, capture, err := h.br.CheckURL(ctx, url, checklist)
	if err != nil {
		if errors.Is(err, ErrBrowser) {
			return CheckResult{}, ErrBrowser
		}
		return CheckResult{Passed: false, Checks: checks, Reason: "browser"}, nil
	}
	checks = mergeChecks(checks, extra)
	return CheckResult{Passed: allPassed(checks), Checks: checks, Reason: firstFailure(checks), Capture: capture}, nil
}

// allPassedExceptLayout reports whether every static dimension passes. Layout
// and errors need the browser, so they start true in the static map.
func allPassedExceptLayout(checks []result.MockCheck) bool {
	for _, check := range checks {
		if check.ID == "layout_390" || check.ID == "layout_1280" {
			continue
		}
		if !check.Passed {
			return false
		}
	}
	return true
}

func firstFailure(checks []result.MockCheck) string {
	for _, check := range checks {
		if !check.Passed {
			return check.ID
		}
	}
	return ""
}
