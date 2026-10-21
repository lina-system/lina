// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/secret"
)

const testToken = "123456:synthetic_telegram_credential"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestCredentialOnlyReachesTelegramTransport(t *testing.T) {
	var wire *http.Request
	transport := credentialTransport{token: secret.New(testToken), next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		wire = r
		return response(r, 200, `{"ok":true,"result":[]}`), nil
	})}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", "https://api.telegram.org/bot0:opaque/getUpdates", nil)
	res, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if wire.URL.Path != "/bot"+testToken+"/getUpdates" {
		t.Fatalf("credential not injected at send boundary")
	}
	if strings.Contains(req.URL.String(), testToken) || strings.Contains(res.Request.URL.String(), testToken) {
		t.Fatal("credential escaped in caller's request or response metadata")
	}
	for _, v := range []any{transport, &transport} {
		data, _ := json.Marshal(v)
		if strings.Contains(fmt.Sprintf("%+v %#v %s", v, v, data), testToken) {
			t.Fatal("credential escaped through formatting")
		}
	}
}

func TestCredentialTransportRejectsOtherDestinations(t *testing.T) {
	for _, address := range []string{"http://api.telegram.org/bot0:opaque/getUpdates", "https://example.invalid/bot0:opaque/getUpdates", "https://api.telegram.org:444/bot0:opaque/getUpdates", "https://user@api.telegram.org/bot0:opaque/getUpdates", "https://api.telegram.org/bot0:opaque/getUpdates?x=y"} {
		t.Run(address, func(t *testing.T) {
			transport := credentialTransport{token: secret.New(testToken), next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				t.Fatal("sent to untrusted destination")
				return nil, nil
			})}
			req, _ := http.NewRequestWithContext(t.Context(), "POST", address, nil)
			if _, err := transport.RoundTrip(req); err == nil {
				t.Fatal("accepted untrusted destination")
			}
		})
	}
}

func TestClientConstructionOfflineAndSafeErrors(t *testing.T) {
	for _, mode := range []string{"network", "api", "malformed", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c, err := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("unexpected retry or redirect")
				}
				io.Copy(io.Discard, r.Body)
				switch mode {
				case "network":
					return nil, &url.Error{Op: "POST", URL: r.URL.String(), Err: errors.New(testToken)}
				case "api":
					return response(r, 401, `{"ok":false,"error_code":401,"description":"`+testToken+`"}`), nil
				case "malformed":
					return response(r, 200, testToken), nil
				default:
					res := response(r, 307, "")
					res.Header.Set("Location", "https://example.invalid/"+testToken)
					return res, nil
				}
			}))
			if err != nil || calls != 0 {
				t.Fatalf("constructor contacted platform: %v, calls=%d", err, calls)
			}
			_, err = c.call(t.Context(), "getUpdates", map[string]any{"timeout": 25})
			if err == nil {
				t.Fatal("expected sanitized failure")
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				if strings.Contains(fmt.Sprintf("%+v %#v", e, e), testToken) {
					t.Fatal("credential in error chain")
				}
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestPollRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	c, err := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.call(ctx, "getUpdates", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestBotTokenDoesNotEnterChildEnvironment(t *testing.T) {
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		io.Copy(io.Discard, r.Body)
		return response(r, 200, `{"ok":true,"result":[]}`), nil
	}))
	if _, err := c.call(t.Context(), "getUpdates", nil); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	l := lease.New(func() {})
	defer l.Release()
	var output bytes.Buffer
	err = owner.Run(t.Context(), l, proc.Command{
		Path: executable, Args: []string{"-test.run=^TestTelegramChildEnvironment$"},
		Env: append(proc.InheritedEnvironment(), "LINA_TELEGRAM_ENV_CHILD=1"), Stdout: &output, Stderr: &output,
	})
	if err != nil || strings.Contains(output.String(), testToken) {
		t.Fatalf("child environment check: %v", err)
	}
}

func TestTelegramChildEnvironment(t *testing.T) {
	if os.Getenv("LINA_TELEGRAM_ENV_CHILD") != "1" {
		return
	}
	// Report a code rather than dumping any inherited credentials.
	for _, entry := range os.Environ() {
		if strings.Contains(entry, testToken) {
			os.Exit(9)
		}
	}
	os.Exit(0)
}
