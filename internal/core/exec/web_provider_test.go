// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/secret"
)

type webTransport func(*http.Request) (*http.Response, error)

func (f webTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func webResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestFirecrawlSearchAndReadableExtract(t *testing.T) {
	calls := 0
	p := NewFirecrawl(secret.New("fc-synthetic-key"), webTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "api.firecrawl.dev" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fc-synthetic-key" {
			t.Error("wrong credential destination")
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v2/search":
			if !strings.Contains(string(body), `"query":"latest X"`) || !strings.Contains(string(body), `"limit":3`) {
				t.Error(string(body))
			}
			return webResponse(200, `{"success":true,"data":{"web":[{"title":"X","url":"https://example.com/x","description":"Latest X"}]}}`), nil
		case "/v2/scrape":
			if !strings.Contains(string(body), `"formats":["markdown"]`) {
				t.Error(string(body))
			}
			return webResponse(200, `{"success":true,"data":{"markdown":"Readable X page","metadata":{"sourceURL":"https://example.com/x"}}}`), nil
		}
		t.Error("unexpected path")
		return nil, errors.New("bad path")
	}))
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	hits, err := p.Search(t.Context(), "latest X", 3)
	if err != nil || len(hits) != 1 || hits[0].Snippet != "Latest X" {
		t.Fatal(hits, err)
	}
	text, err := p.Extract(t.Context(), "https://example.com/x")
	if err != nil || text != "Readable X page" || calls != 2 {
		t.Fatal(text, err, calls)
	}
}

func TestFirecrawlRefusesPrivateDNSAndUnsafeFinalURL(t *testing.T) {
	calls := 0
	p := NewFirecrawl(secret.New("fc-synthetic-key"), webTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return webResponse(200, `{"success":true,"data":{"markdown":"private","metadata":{"sourceURL":"http://127.0.0.1/"}}}`), nil
	}))
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := p.Extract(t.Context(), "https://example.com/x"); err == nil || calls != 0 {
		t.Fatal("private DNS reached provider")
	}
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	if text, err := p.Extract(t.Context(), "https://example.com/x"); err == nil || text != "" || calls != 1 {
		t.Fatal("unsafe final URL accepted")
	}
}

func TestFirecrawlBoundsAndSanitizesFailures(t *testing.T) {
	for _, body := range []string{`{"success":false,"error":"fc-synthetic-key"}`, `not-json fc-synthetic-key`, strings.Repeat("x", (2<<20)+1)} {
		p := NewFirecrawl(secret.New("fc-synthetic-key"), webTransport(func(*http.Request) (*http.Response, error) { return webResponse(200, body), nil }))
		if _, err := p.Search(t.Context(), "x", 1); err == nil || strings.Contains(err.Error(), "fc-synthetic-key") {
			t.Fatal("response failure escaped", err)
		}
	}
	p := NewFirecrawl(secret.New("fc-synthetic-key"), webTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Search(ctx, "x", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	p = NewFirecrawl(secret.New("fc-synthetic-key"), webTransport(func(*http.Request) (*http.Response, error) {
		resp := webResponse(302, "")
		resp.Header.Set("Location", "https://example.com/steal")
		return resp, nil
	}))
	if _, err := p.Search(t.Context(), "x", 1); err == nil {
		t.Fatal("provider redirect followed")
	}
}

func TestFirecrawlRedactsDecodedResponseStrings(t *testing.T) {
	for _, tc := range []struct{ name, key, wire string }{
		{"literal", "fc-synthetic-key", "fc-synthetic-key"},
		{"escaped_prefix", "fc-synthetic-key", `\u0066c-synthetic-key`},
		{"escaped_entire_key", "fc-synthetic-key", `\u0066\u0063\u002d\u0073\u0079\u006e\u0074\u0068\u0065\u0074\u0069\u0063\u002d\u006b\u0065\u0079`},
		{"json_syntax", `fc-"quoted\key`, `fc-\"quoted\\key`},
		{"schema_word", "success", `\u0073uccess`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewFirecrawl(secret.New(tc.key), webTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer "+tc.key {
					t.Error("credential changed before HTTP injection")
				}
				switch r.URL.Path {
				case "/v2/search":
					return webResponse(200, fmt.Sprintf(`{"success":true,"data":{"web":[{"title":"Title %s / %s","url":"https://93.184.216.34/%s","description":"본문 %s"},{"title":"Unchanged","url":"https://example.com/","description":"Safe text"}]}}`, tc.wire, tc.wire, tc.wire, tc.wire)), nil
				case "/v2/scrape":
					return webResponse(200, fmt.Sprintf(`{"success":true,"data":{"markdown":"본문 %s / %s","metadata":{"sourceURL":"https://93.184.216.34/%s"}}}`, tc.wire, tc.wire, tc.wire)), nil
				}
				t.Error("unexpected endpoint")
				return nil, errors.New("unexpected endpoint")
			}))
			t.Run("search", func(t *testing.T) {
				hits, err := p.Search(t.Context(), "latest X", 2)
				if err != nil || len(hits) != 2 {
					t.Fatalf("search failed: hits=%d err=%v", len(hits), err)
				}
				want := WebHit{Title: "Title [REDACTED] / [REDACTED]", URL: "https://93.184.216.34/[REDACTED]", Snippet: "본문 [REDACTED]"}
				if hits[0] != want {
					t.Error("decoded search fields were not redacted")
				}
				if hits[1] != (WebHit{Title: "Unchanged", URL: "https://example.com/", Snippet: "Safe text"}) {
					t.Error("unrelated search fields changed")
				}
			})
			t.Run("extract", func(t *testing.T) {
				text, err := p.Extract(t.Context(), "https://93.184.216.34/x")
				if err != nil || text != "본문 [REDACTED] / [REDACTED]" {
					t.Errorf("decoded extraction was not redacted: err=%v", err)
				}
			})
		})
	}
}
