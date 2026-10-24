// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/thisisjun786/lina/kit/secret"
)

var (
	// ErrUnauthorized means the token was refused; the connection needs a new
	// credential from the person.
	ErrUnauthorized = errors.New("discord_unauthorized")
	// ErrForbidden means the bot lacks access to that resource.
	ErrForbidden = errors.New("discord_forbidden")
	// ErrNotFound means Discord does not know the resource.
	ErrNotFound = errors.New("discord_not_found")
	// ErrUnavailable covers transport failures and malformed responses.
	ErrUnavailable = errors.New("discord_unavailable")
)

// ReadLimit bounds one REST read. Message pages stay far below it.
const ReadLimit = 8 << 20

// Client is Discord's read-only REST surface. One implementation serves the
// messenger source; a page keeps its cursor, so a rate-limited read waits for
// the platform's retry-after and then continues from where it was.
type Client struct {
	token secret.Secret
	http  *http.Client
	// Attempts bounds how many platform waits one call may honour.
	Attempts int
}

// NewClient binds a read client to one bot token and the fixed API origin.
func NewClient(token secret.Secret, tr http.RoundTripper) (*Client, error) {
	if !token.Ready() {
		return nil, ErrUnavailable
	}
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &Client{token: token, Attempts: 3, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable }}}, nil
}

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Get reads one absolute API path with its query and decodes the JSON body.
// Redirects are refused, so the credential cannot leave Discord.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		if attempt >= c.Attempts {
			return ErrUnavailable
		}
		site := "https://discord.com/api/v10" + path
		if len(query) > 0 {
			site += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, site, nil)
		if err != nil {
			return ErrUnavailable
		}
		if err = secret.InjectHeader(req.Header, "Authorization", "Bot ", c.token); err != nil {
			return ErrUnavailable
		}
		req.Header.Set("User-Agent", "LINA (https://github.com/thisisjun786/lina)")
		resp, err := c.http.Do(req)
		if err != nil {
			return ErrUnavailable
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, ReadLimit))
		resp.Body.Close()
		if readErr != nil {
			return ErrUnavailable
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			var limited struct {
				Retry float64 `json:"retry_after"`
			}
			if json.Unmarshal(body, &limited) != nil || limited.Retry <= 0 || math.IsNaN(limited.Retry) || math.IsInf(limited.Retry, 0) || limited.Retry > 3600 {
				return ErrUnavailable
			}
			timer := time.NewTimer(time.Duration(math.Ceil(limited.Retry * float64(time.Second))))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return ErrUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			return ErrForbidden
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			return ErrUnavailable
		}
		if out == nil {
			return nil
		}
		if json.Unmarshal(body, out) != nil {
			return ErrUnavailable
		}
		return nil
	}
}
