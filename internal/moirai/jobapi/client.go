// SPDX-License-Identifier: Apache-2.0
package jobapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

// Client speaks only to the installed Unix socket. It uses no HTTP proxy,
// cookies, authorization headers, provider environment or credential store.
type Client struct{ http *http.Client }

func NewClient(socket string) *Client {
	tr := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: tr, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type Reply struct {
	Result json.RawMessage
	Error  string
}
type ReportRequest struct {
	JobID string
	State JobState
}

func (c *Client) call(ctx context.Context, method string, arg, result any) error {
	data, err := json.Marshal(arg)
	if err != nil {
		return ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://core/"+method, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	data, err = io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || res.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	var reply Reply
	if json.Unmarshal(data, &reply) != nil {
		return ErrInvalid
	}
	if reply.Error != "" {
		for _, e := range []error{ErrInvalid, ErrEpoch, ErrConflict, ErrDisabled, ErrBudget, ErrUnavailable, ErrPending, ErrNotDue} {
			if e.Error() == reply.Error {
				return e
			}
		}
		return ErrUnavailable
	}
	if result != nil {
		return json.Unmarshal(reply.Result, result)
	}
	return nil
}
func (c *Client) Aux(ctx context.Context, r AuxRequest) (AuxResult, error) {
	var out AuxResult
	err := c.call(ctx, "Aux", r, &out)
	return out, err
}
func (c *Client) Apply(ctx context.Context, p Proposal) (Applied, error) {
	var out Applied
	err := c.call(ctx, "Apply", p, &out)
	return out, err
}
func (c *Client) Report(ctx context.Context, id string, state JobState) error {
	return c.call(ctx, "Report", ReportRequest{id, state}, nil)
}

// Ready checks the private job protocol without admitting or replaying a job.
func (c *Client) Ready(ctx context.Context) error {
	var result struct {
		Protocol string `json:"protocol"`
	}
	if err := c.call(ctx, "Ready", struct{}{}, &result); err != nil {
		return err
	}
	if result.Protocol != "moirai/1" {
		return ErrInvalid
	}
	return nil
}
