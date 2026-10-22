// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
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

func TestFirecrawlRedactsDecodedCredentials(t *testing.T) {
	for _, key := range []string{"fc-synthetic-key", `fc-synthetic-"quoted"\key/`} {
		for _, encoding := range []string{"literal", "partial_escape", "unicode_escape"} {
			t.Run(fmt.Sprintf("%s/%s", key, encoding), func(t *testing.T) {
				encoded, err := json.Marshal(key)
				if err != nil {
					t.Fatal(err)
				}
				wireKey := string(encoded[1 : len(encoded)-1])
				switch encoding {
				case "partial_escape":
					wireKey = strings.Replace(wireKey, "f", `\u0066`, 1)
				case "unicode_escape":
					wireKey = ""
					for _, c := range key {
						wireKey += fmt.Sprintf(`\u%04x`, c)
					}
				}
				p := NewFirecrawl(secret.New(key), webTransport(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Authorization") != "Bearer "+key {
						t.Error("provider credential changed")
					}
					if r.URL.Path == "/v2/search" {
						return webResponse(200, fmt.Sprintf(`{"success":true,"data":{"web":[{"title":"Title %s","url":"https://93.184.216.34/%s","description":"Snippet %s / %s"},{"title":"Other %s","url":"https://93.184.216.34/clean","description":"Clean"}]}}`, wireKey, wireKey, wireKey, wireKey, wireKey)), nil
					}
					return webResponse(200, fmt.Sprintf(`{"success":true,"data":{"markdown":"Page %s / %s","metadata":{"sourceURL":"https://93.184.216.34/final"}}}`, wireKey, wireKey)), nil
				}))
				hits, err := p.Search(t.Context(), "latest X", 2)
				if err != nil || len(hits) != 2 {
					t.Fatalf("search failed: %v", err)
				}
				want := []WebHit{
					{Title: "Title [REDACTED]", URL: "https://93.184.216.34/[REDACTED]", Snippet: "Snippet [REDACTED] / [REDACTED]"},
					{Title: "Other [REDACTED]", URL: "https://93.184.216.34/clean", Snippet: "Clean"},
				}
				for i := range hits {
					if hits[i] != want[i] {
						t.Errorf("search hit %d did not redact decoded credentials and preserve surrounding text", i)
					}
				}
				text, err := p.Extract(t.Context(), "https://93.184.216.34/page")
				if err != nil || text != "Page [REDACTED] / [REDACTED]" {
					t.Errorf("extract did not redact decoded credentials and preserve surrounding text: %v", err)
				}
			})
		}
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

func TestFirecrawlRedactsFinalURLBeforeDNS(t *testing.T) {
	p := NewFirecrawl(secret.New("fc-synthetic-key"), webTransport(func(*http.Request) (*http.Response, error) {
		return webResponse(200, `{"success":true,"data":{"markdown":"Page","metadata":{"sourceURL":"https://\u0066c-synthetic-key.example.com/"}}}`), nil
	}))
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		t.Error("reflected credential reached final URL DNS lookup")
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	if text, err := p.Extract(t.Context(), "https://93.184.216.34/page"); text != "" || !errors.Is(err, ErrWebUnavailable) {
		t.Error("credential-bearing final hostname accepted")
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
