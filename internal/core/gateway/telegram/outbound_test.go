// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/secret"
)

func TestSendErrorsNeverRetryUnknownOutcome(t *testing.T) {
	for _, tc := range []struct {
		code               int
		description, class string
		retry              time.Duration
	}{
		{400, "message is too long", "too_long", 0},
		{400, "bad request", "rejected", 0},
		{401, "Unauthorized", "unauthorized", 0},
		{403, "bot blocked", "blocked", 0},
		{429, "rate limit", "retry_later", 8 * time.Second},
		{500, "server failed", "unknown", 0},
	} {
		t.Run(tc.class, func(t *testing.T) {
			calls := 0
			c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				io.Copy(io.Discard, r.Body)
				return response(r, tc.code, fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q,"parameters":{"retry_after":8}}`, tc.code, tc.description)), nil
			}))
			_, err := c.sendMessage(t.Context(), 12, 3, "test", nil)
			var f *failure
			if !errors.As(err, &f) || f.class != tc.class || f.retryAfter != tc.retry || calls != 1 {
				t.Fatalf("result=%v calls=%d", err, calls)
			}
		})
	}
}

func TestSendUsesPlainTextAndThread(t *testing.T) {
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("chat_id") != "12" || r.FormValue("message_thread_id") != "3" || r.FormValue("text") != "<plain> & text" || r.FormValue("parse_mode") != "" {
			t.Fatal("message destination or text changed")
		}
		return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
	}))
	id, err := c.sendMessage(t.Context(), 12, 3, "<plain> & text", nil)
	if err != nil || id != 19 {
		t.Fatalf("send=%d %v", id, err)
	}
}

func TestApprovalCannotSendTruncatedArguments(t *testing.T) {
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("sent oversized approval"); return nil, nil }))
	markup, _ := approvalButtons("request_123")
	_, err := c.sendMessage(t.Context(), 12, 0, strings.Repeat("x", 4097), &markup)
	var f *failure
	if !errors.As(err, &f) || f.class != "too_long" {
		t.Fatalf("oversized approval=%v", err)
	}
}
