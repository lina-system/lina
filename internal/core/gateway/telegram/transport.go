// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/kit/secret"
)

const (
	apiOrigin        = "https://api.telegram.org"
	opaqueToken      = "0:opaque"
	maxResponseBytes = 8 << 20
)

var (
	errCredential = errors.New("telegram_invalid_credential")
	errTransport  = errors.New("telegram_transport_failed")
	errResponse   = errors.New("telegram_invalid_response")
	tokenPattern  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
)

// credentialTransport is the only plaintext credential boundary. The library
// sees a placeholder token; only the private request passed to net/http carries
// the real token. Neither requests nor errors containing it escape to Core.
type credentialTransport struct {
	token secret.Secret
	next  http.RoundTripper
}

func (t credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.telegram.org" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.Fragment != "" {
		return nil, errCredential
	}
	prefix := "/bot" + opaqueToken + "/"
	if strings.HasPrefix(req.URL.Path, "/file/bot"+opaqueToken+"/") {
		prefix = "/file/bot" + opaqueToken + "/"
	}
	if !strings.HasPrefix(req.URL.Path, prefix) {
		return nil, errCredential
	}
	// Reuse the secret package's authorized HTTP injection boundary. This
	// temporary header is never attached to any request or sent over the wire.
	h := make(http.Header)
	if secret.InjectHeader(h, "Authorization", "", t.token) != nil {
		return nil, errCredential
	}
	raw := h.Get("Authorization")
	if len(raw) > 256 || !tokenPattern.MatchString(raw) {
		return nil, errCredential
	}
	wire := req.Clone(req.Context())
	wire.URL.Path = strings.Replace(req.URL.Path, "bot"+opaqueToken+"/", "bot"+raw+"/", 1)
	wire.URL.RawPath = ""
	// Telegram effects must not be transparently replayed by net/http.
	wire.GetBody = nil
	wire.Header.Del("Idempotency-Key")
	wire.Header.Del("X-Idempotency-Key")
	res, err := t.next.RoundTrip(wire)
	if err != nil {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		return nil, errTransport
	}
	if res == nil || res.Body == nil {
		return nil, errResponse
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		res.Body.Close()
		return nil, errTransport
	}
	res.Request = req
	res.Header.Del("Location")
	limit := int64(maxResponseBytes)
	if strings.HasPrefix(prefix, "/file/") {
		limit = 20 << 20
	}
	res.Body = &safeBody{ReadCloser: res.Body, remaining: limit}
	return res, nil
}

// A server or transport can put a credential in a read error as well as in a
// connection error. Limit reads and replace all errors except EOF.
type safeBody struct {
	io.ReadCloser
	remaining int64
}

func (r *safeBody) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.ReadCloser.Read(probe[:])
		if n == 0 && err == io.EOF {
			return 0, io.EOF
		}
		return 0, errResponse
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	if err != nil && err != io.EOF {
		err = errTransport
	}
	return n, err
}

func (r *safeBody) Close() error {
	if r.ReadCloser.Close() != nil {
		return errTransport
	}
	return nil
}

type failure struct {
	class      string
	retryAfter time.Duration
}

func (e *failure) Error() string { return "telegram_" + e.class }

type client struct {
	api   *gotgbot.BaseBotClient
	http  *http.Client
	token secret.Secret
	pause func(context.Context, time.Duration) error
}

func newClient(token secret.Secret, next http.RoundTripper) (*client, error) {
	// Construction deliberately performs no GetMe, network request or goroutine
	// start. Platform availability must never participate in Core readiness.
	if next == nil {
		next = http.DefaultTransport.(*http.Transport).Clone()
	}
	h := &http.Client{
		Transport:     credentialTransport{token: token, next: next},
		Timeout:       35 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errTransport },
	}
	return &client{api: &gotgbot.BaseBotClient{Client: *h}, http: h, token: token, pause: pause}, nil
}

func (c *client) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	data, err := c.api.RequestWithContext(ctx, opaqueToken, method, params, &gotgbot.RequestOpts{Timeout: 35 * time.Second, APIURL: apiOrigin})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil {
		return c.scrub(data)
	}
	var api *gotgbot.TelegramError
	if errors.As(err, &api) {
		switch api.Code {
		case 401:
			return nil, &failure{class: "unauthorized"}
		case 403:
			return nil, &failure{class: "blocked"}
		case 429:
			delay := time.Second
			if api.ResponseParams != nil && api.ResponseParams.RetryAfter > 0 && api.ResponseParams.RetryAfter <= 86400 {
				delay = time.Duration(api.ResponseParams.RetryAfter) * time.Second
			}
			return nil, &failure{class: "retry_later", retryAfter: delay}
		case 400:
			if strings.Contains(strings.ToLower(api.Description), "message is too long") {
				return nil, &failure{class: "too_long"}
			}
			return nil, &failure{class: "rejected"}
		}
	}
	// A timeout, broken response or server failure may follow delivery. Never
	// wrap library errors: those include request arguments and platform prose.
	return nil, &failure{class: "unknown"}
}

// Decode before masking so escaped JSON credentials are covered too. No raw
// platform payload or description is passed to an event or ledger writer.
func (c *client) scrub(data json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, errResponse
	}
	h := make(http.Header)
	if secret.InjectHeader(h, "Authorization", "", c.token) != nil {
		return nil, errCredential
	}
	raw := h.Get("Authorization")
	var clean func(any) any
	clean = func(v any) any {
		switch v := v.(type) {
		case string:
			return strings.ReplaceAll(v, raw, secret.Placeholder)
		case []any:
			for i := range v {
				v[i] = clean(v[i])
			}
		case map[string]any:
			for k, child := range v {
				delete(v, k)
				v[strings.ReplaceAll(k, raw, secret.Placeholder)] = clean(child)
			}
		}
		return v
	}
	masked, err := json.Marshal(clean(value))
	if err != nil {
		return nil, errResponse
	}
	return masked, nil
}
