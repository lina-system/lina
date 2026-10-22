// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/secret"
)

var ErrWebUnavailable = errors.New("web_provider_unavailable")

// Firecrawl adapts Hermes' search metadata and Markdown extraction behavior.
// Its destination is fixed; transport injection is only an in-process test seam.
type Firecrawl struct {
	key    secret.Secret
	client *http.Client
	lookup func(context.Context, string, string) ([]netip.Addr, error)
}

// Only these provider fields are consumed. Redact them together at the HTTP
// boundary, after decoding and before tool results or URL validation see them.
type firecrawlResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Web      []struct{ Title, URL, Description string } `json:"web"`
		Markdown string                                     `json:"markdown"`
		Metadata struct {
			SourceURL string `json:"sourceURL"`
		} `json:"metadata"`
	} `json:"data"`
}

func NewFirecrawl(key secret.Secret, transport http.RoundTripper) *Firecrawl {
	return &Firecrawl{key: key, client: &http.Client{Transport: transport, Timeout: 55 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, lookup: net.DefaultResolver.LookupNetIP}
}
func (p *Firecrawl) request(ctx context.Context, path string, input any, out *firecrawlResponse) error {
	body, err := json.Marshal(input)
	if err != nil {
		return ErrWebUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.firecrawl.dev/v2/"+path, bytes.NewReader(body))
	if err != nil {
		return ErrWebUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if secret.InjectHeader(req.Header, "Authorization", "Bearer ", p.key) != nil {
		return ErrWebUnavailable
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrWebUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrWebUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrWebUnavailable
	}
	if len(raw) > 2<<20 {
		return ErrWebUnavailable
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrWebUnavailable
	}
	// JSON escapes can conceal the key on the wire. Only decoded strings are
	// safe to redact; the plaintext key stays at this HTTP boundary.
	key := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if key != "" {
		redact := func(s string) string { return strings.ReplaceAll(s, key, secret.Placeholder) }
		for i := range out.Data.Web {
			hit := &out.Data.Web[i]
			hit.Title = redact(hit.Title)
			hit.URL = redact(hit.URL)
			hit.Description = redact(hit.Description)
		}
		out.Data.Markdown = redact(out.Data.Markdown)
		out.Data.Metadata.SourceURL = redact(out.Data.Metadata.SourceURL)
	}
	return nil
}
func (p *Firecrawl) Search(ctx context.Context, query string, limit int) ([]WebHit, error) {
	var response firecrawlResponse
	if err := p.request(ctx, "search", struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}{query, limit}, &response); err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, ErrWebUnavailable
	}
	hits := make([]WebHit, 0, min(limit, len(response.Data.Web)))
	for _, hit := range response.Data.Web {
		if len(hits) >= limit {
			break
		}
		hits = append(hits, WebHit{Title: hit.Title, URL: hit.URL, Snippet: hit.Description})
	}
	return hits, nil
}
func (p *Firecrawl) publicURL(ctx context.Context, raw string) bool {
	if !PublicWebURL(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		return PublicWebIP(ip)
	}
	addresses, err := p.lookup(ctx, "ip", u.Hostname())
	if err != nil || len(addresses) == 0 {
		return false
	}
	for _, ip := range addresses {
		if !PublicWebIP(ip) {
			return false
		}
	}
	return true
}
func (p *Firecrawl) Extract(ctx context.Context, url string) (string, error) {
	if !p.publicURL(ctx, url) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrWebUnavailable
	}
	var response firecrawlResponse
	input := struct {
		URL             string   `json:"url"`
		Formats         []string `json:"formats"`
		OnlyMainContent bool     `json:"onlyMainContent"`
		Timeout         int      `json:"timeout"`
	}{url, []string{"markdown"}, true, 50000}
	if err := p.request(ctx, "scrape", input, &response); err != nil {
		return "", err
	}
	if !response.Success || strings.TrimSpace(response.Data.Markdown) == "" {
		return "", ErrWebUnavailable
	}
	if final := response.Data.Metadata.SourceURL; final != "" && !p.publicURL(ctx, final) {
		return "", ErrWebUnavailable
	}
	return response.Data.Markdown, nil
}
