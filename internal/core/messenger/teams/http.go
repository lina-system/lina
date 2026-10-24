// SPDX-License-Identifier: Apache-2.0
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/secret"
)

const graph = "https://graph.microsoft.com/v1.0"
const login = "https://login.microsoftonline.com/"

var (
	ErrInvalid     = errors.New("invalid_teams_configuration_or_response")
	ErrDestination = errors.New("teams_destination_not_allowed")
	ErrSignIn      = errors.New("teams_needs_sign_in")
	ErrUnsupported = errors.New("unsupported: Teams requires tenant approval and delegated read consent")
	ErrUnavailable = errors.New("teams_unavailable")
	ErrNotFound    = errors.New("teams_resource_not_found")
)

// RetryError preserves Graph's retry deadline without holding a source worker.
type RetryError struct{ After time.Duration }

func (e *RetryError) Error() string { return "teams_rate_limited" }

func client(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func readBody(r *http.Response) ([]byte, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return nil, ErrUnavailable
	}
	return b, nil
}
func collectionURL(target, path string) bool {
	u, err := url.Parse(target)
	return err == nil && u.Scheme == "https" && u.Host == "graph.microsoft.com" && u.User == nil && u.Fragment == "" && u.Path == path
}
func nextURL(next, path string) error {
	if next != "" && !collectionURL(next, path) {
		return ErrDestination
	}
	return nil
}
func retryAfter(h http.Header, now time.Time) time.Duration {
	if n, err := strconv.ParseInt(h.Get("Retry-After"), 10, 32); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if at, err := http.ParseTime(h.Get("Retry-After")); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return time.Second
}
func (a *Adapter) get(ctx context.Context, target string, out any) error {
	u, err := url.Parse(target)
	if err != nil || !collectionURL(target, u.Path) || !strings.HasPrefix(u.Path, "/v1.0/") {
		return ErrDestination
	}
	refreshed := false
	force := false
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return ErrInvalid
		}
		token, err := a.cfg.Token(ctx, force)
		force = false
		if err != nil {
			return err
		}
		if err = secret.InjectHeader(req.Header, "Authorization", "Bearer ", token); err != nil {
			return ErrSignIn
		}
		resp, err := a.client.Do(req)
		if err != nil {
			return ErrUnavailable
		}
		body, err := readBody(resp)
		if err != nil {
			return err
		}
		if resp.StatusCode == 401 && !refreshed {
			refreshed = true
			force = true
			continue
		}
		switch resp.StatusCode {
		case 200:
			if json.Unmarshal(body, out) != nil {
				return ErrInvalid
			}
			return nil
		case 401:
			return ErrSignIn
		case 404:
			return ErrNotFound
		case 403:
			return ErrUnsupported
		case 429, 503:
			delay := retryAfter(resp.Header, time.Now())
			if delay > 30*time.Second || attempt == 2 {
				return &RetryError{After: delay}
			}
			if err = a.wait(ctx, delay); err != nil {
				return err
			}
		default:
			return ErrUnavailable
		}
	}
	return ErrUnavailable
}

// pages requires each continuation to name the same collection, preventing a
// Graph response from using Core's token to read another resource or origin.
func (a *Adapter) pages(ctx context.Context, target string, visit func(json.RawMessage) error) error {
	u, _ := url.Parse(target)
	seen := map[string]bool{}
	for target != "" {
		if len(seen) >= 10000 || seen[target] {
			return ErrInvalid
		}
		seen[target] = true
		var page struct {
			Value []json.RawMessage `json:"value"`
			Next  string            `json:"@odata.nextLink"`
		}
		if err := a.get(ctx, target, &page); err != nil {
			return err
		}
		if page.Value == nil {
			return ErrInvalid
		}
		if err := nextURL(page.Next, u.Path); err != nil {
			return err
		}
		for _, v := range page.Value {
			if err := visit(v); err != nil {
				return err
			}
		}
		target = page.Next
	}
	return nil
}
