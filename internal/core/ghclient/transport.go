// SPDX-License-Identifier: Apache-2.0
package ghclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

const maxOutput = 8 << 20
const pollInterval = 30 * time.Second
const maxPages = 100

type bounded struct {
	bytes.Buffer
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	n := len(p)
	if n > maxOutput-b.Len() {
		p = p[:maxOutput-b.Len()]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

type cached struct {
	etag string
	body []byte
}
type observation struct {
	bytes       int
	responses   map[string]cached
	snapshot    Snapshot
	reviewsHead string
}
type readError struct {
	cause string
	retry time.Duration
}

func (e *readError) Error() string { return "GitHub observation: " + e.cause }
func seconds(value string) time.Duration {
	n, e := strconv.ParseInt(value, 10, 32)
	if e != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
func retryDelay(h http.Header) time.Duration {
	d := pollInterval
	if v := seconds(h.Get("X-Poll-Interval")); v > 0 {
		d = v
	}
	if v := seconds(h.Get("Retry-After")); v > d {
		d = v
	}
	if at, e := http.ParseTime(h.Get("Retry-After")); e == nil && time.Until(at) > d {
		d = time.Until(at)
	}
	if h.Get("X-Ratelimit-Remaining") == "0" {
		if epoch, e := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64); e == nil && epoch > 0 {
			if v := time.Until(time.Unix(epoch, 0)); v > d {
				d = v
			}
		}
	}
	return d
}
func (c *Client) request(ctx context.Context, endpoint, query string, cache *observation) ([]byte, time.Duration, error) {
	if c.cfg.Path == "" {
		return nil, 0, &readError{cause: "gh_unavailable"}
	}
	method := "GET"
	if query != "" {
		method = "POST"
	}
	args := []string{"api", "--hostname", "github.com", "--method", method, "--include", "-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2022-11-28", endpoint}
	key := endpoint + query
	old := cache.responses[key]
	if query != "" {
		args = append(args, "-f", "query="+query)
	} else if old.etag != "" {
		args = append(args, "-H", "If-None-Match: "+old.etag)
	}
	l, err := c.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "GitHub pull request observation"})
	if err != nil {
		return nil, 0, err
	}
	defer l.Release()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var output bounded
	runErr := c.runner.Run(deadline, l, proc.Command{Path: c.cfg.Path, Args: args, Env: c.cfg.Env, Stdout: &output, Stderr: io.Discard})
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	if output.overflow {
		return nil, 0, &readError{cause: "response_too_large"}
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(output.Bytes())), nil)
	if err != nil {
		return nil, 0, &readError{cause: "gh_failed"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, 0, &readError{cause: "invalid_response"}
	}
	cache.bytes += len(body)
	if cache.bytes > 32<<20 {
		return nil, 0, &readError{cause: "response_too_large"}
	}
	delay := retryDelay(response.Header)
	var diagnostic struct {
		Message string
		Errors  []struct{ Type string }
	}
	_ = json.Unmarshal(body, &diagnostic)
	limited := response.StatusCode == 429 || response.StatusCode == 403 && (response.Header.Get("X-Ratelimit-Remaining") == "0" || response.Header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(diagnostic.Message), "rate limit"))
	for _, e := range diagnostic.Errors {
		limited = limited || e.Type == "RATE_LIMITED"
	}
	if limited {
		if delay < time.Minute && response.Header.Get("Retry-After") == "" {
			delay = time.Minute
		}
		return nil, delay, &readError{cause: "rate_limited", retry: delay}
	}
	if response.StatusCode == 304 {
		if old.body == nil {
			return nil, delay, &readError{cause: "uncached_not_modified"}
		}
		return old.body, delay, nil
	}
	if response.StatusCode != 200 || runErr != nil || len(diagnostic.Errors) > 0 {
		cause := "read_failed"
		if response.StatusCode == 401 {
			cause = "needs_sign_in"
		}
		if response.StatusCode == 403 {
			cause = "permission_denied"
		}
		if response.StatusCode == 404 {
			cause = "not_found"
		}
		return nil, delay, &readError{cause: cause}
	}
	if cache.responses == nil {
		cache.responses = make(map[string]cached)
	}
	if query == "" {
		etag := response.Header.Get("ETag")
		if len(etag) > 1024 || strings.ContainsAny(etag, "\r\n") {
			return nil, delay, errors.New("invalid GitHub validator")
		}
		cache.responses[key] = cached{etag, body}
	}
	return body, delay, nil
}
func decode(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return &readError{cause: "invalid_response"}
	}
	return nil
}
func endpoint(ref Ref) string { return fmt.Sprintf("repos/%s/pulls/%d", ref.Repository, ref.Number) }
