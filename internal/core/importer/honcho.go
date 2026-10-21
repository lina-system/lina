// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/secret"
)

var ErrReadOnly = errors.New("import_source_read_only")

// GETOnlyTransport is the final guard before the HTTP transport. Redirects are
// disabled separately; even redirected requests must pass this method guard.
type GETOnlyTransport struct{ Base http.RoundTripper }

func (t GETOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.Method != http.MethodGet || r.Body != nil && r.Body != http.NoBody {
		return nil, ErrReadOnly
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// Honcho holds a run-scoped read credential only in Core. Close releases that
// reference and the transport's idle connections. No credential is serialized
// into a worker job, snapshot, diagnostic, or stored configuration.
type Honcho struct {
	mu         sync.Mutex
	base       *url.URL
	client     *http.Client
	transport  *http.Transport
	credential secret.Secret
	paths      []string
	closed     bool
}

// OpenHoncho compares the published description to an independently verified
// API document before any data request. JSON formatting and object-key order
// are immaterial; every schema value must match. Unknown versions fail closed.
func OpenHoncho(ctx context.Context, address, apiPath string, accepted []byte, credential secret.Secret) (*Honcho, error) {
	expected, err := apiDocument(accepted)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(address)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" {
		return nil, ErrSnapshot
	}
	// A run credential may cross a network only under TLS. Literal loopback
	// addresses retain support for a local Honcho service without relying on DNS.
	if base.Scheme == "http" {
		host, parseErr := netip.ParseAddr(base.Hostname())
		if parseErr != nil || !host.IsLoopback() {
			return nil, ErrReadOnly
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Do not consult environment proxies or a cookie jar during an import.
	tr.Proxy = nil
	h := &Honcho{base: base, transport: tr, credential: credential}
	h.client = &http.Client{Transport: GETOnlyTransport{Base: tr}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := h.get(ctx, apiPath)
	if err != nil {
		h.Close()
		return nil, err
	}
	document, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	closeErr := response.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		h.Close()
		return nil, ErrSnapshot
	}
	if len(document) > 8<<20 {
		h.Close()
		return nil, fmt.Errorf("%w: API description exceeds limit", ErrSchema)
	}
	actual, err := apiDocument(document)
	if err != nil {
		h.Close()
		return nil, err
	}
	if difference := jsonDifference(expected, actual, "$"); difference != "" {
		h.Close()
		return nil, fmt.Errorf("%w: API description differs at %s", ErrSchema, difference)
	}
	for route, entry := range expected["paths"].(map[string]any) {
		if operations, ok := entry.(map[string]any); ok && operations["get"] != nil {
			h.paths = append(h.paths, route)
		}
	}
	sort.Strings(h.paths)
	return h, nil
}

func apiDocument(data []byte) (map[string]any, error) {
	var doc map[string]any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: invalid API description", ErrSchema)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing API description", ErrSchema)
	}
	info, _ := doc["info"].(map[string]any)
	version, _ := info["version"].(string)
	openapi, _ := doc["openapi"].(string)
	paths, _ := doc["paths"].(map[string]any)
	if version == "" || !strings.HasPrefix(openapi, "3.") || len(paths) == 0 {
		return nil, fmt.Errorf("%w: API version or paths missing", ErrSchema)
	}
	return doc, nil
}

func jsonDifference(a, b any, at string) string {
	if reflect.DeepEqual(a, b) {
		return ""
	}
	if am, ok := a.(map[string]any); ok {
		if bm, ok := b.(map[string]any); ok {
			var keys []string
			for k := range am {
				keys = append(keys, k)
			}
			for k := range bm {
				if _, ok := am[k]; !ok {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				av, ao := am[k]
				bv, bo := bm[k]
				if !ao || !bo {
					return at + "." + k
				}
				if diff := jsonDifference(av, bv, at+"."+k); diff != "" {
					return diff
				}
			}
		}
	}
	return at
}

func (h *Honcho) requestURL(relative string) (*url.URL, error) {
	u, err := url.Parse(relative)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\x00") || path.Clean(u.Path) != u.Path {
		return nil, ErrReadOnly
	}
	// Escaped slashes can change routing in a proxy; only ordinary path segments
	// and query parameters are allowed. Query pagination stays on this server.
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return nil, ErrReadOnly
	}
	u.Scheme = h.base.Scheme
	u.Host = h.base.Host
	return u, nil
}
func (h *Honcho) get(ctx context.Context, relative string) (*http.Response, error) {
	u, err := h.requestURL(relative)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrSnapshot
	}
	if err = secret.InjectHeader(req.Header, "Authorization", "Bearer ", h.credential); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	response, err := h.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSnapshot
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("Honcho read: HTTP %d", response.StatusCode)
	}
	return response, nil
}

// Snapshot stores one response exactly as received, including pagination
// metadata. The later ordered import enumerates pages using these bytes; this
// reader never sends a server-supplied URL without checking its origin and path.
func (h *Honcho) Snapshot(ctx context.Context, relative, destination string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrReadOnly
	}
	u, err := h.requestURL(relative)
	if err != nil {
		return err
	}
	allowed := false
	for _, route := range h.paths {
		if routeMatches(route, u.Path) {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrReadOnly
	}
	response, err := h.get(ctx, relative)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return writeSnapshot(ctx, destination, response.Body, "")
}
func routeMatches(template, actual string) bool {
	expected := strings.Split(template, "/")
	got := strings.Split(actual, "/")
	if len(expected) != len(got) {
		return false
	}
	for i, p := range expected {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") && len(p) > 2 {
			if got[i] == "" {
				return false
			}
		} else if p != got[i] {
			return false
		}
	}
	return true
}
func (h *Honcho) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.credential = secret.Secret{}
	h.transport.CloseIdleConnections()
	return nil
}
