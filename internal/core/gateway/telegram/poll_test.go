// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestPollCommitsBeforeAcknowledging(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls, committed := 0, int64(40)
			var offsets []int64
			c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatal(err)
				}
				defer r.MultipartForm.RemoveAll()
				if r.FormValue("offset") != fmt.Sprint(committed) {
					t.Fatal("acknowledged an uncommitted update")
				}
				if r.FormValue("timeout") != "25" {
					t.Fatal("not long polling")
				}
				if calls == 1 {
					return response(r, 200, `{"ok":true,"result":[{"update_id":40},{"update_id":41}]}`), nil
				}
				cancel()
				return nil, ctx.Err()
			}))
			err := c.poll(ctx, committed, func(_ context.Context, u gotgbot.Update, next int64) error {
				if fail && u.UpdateId == 41 {
					return errors.New("store failed: " + testToken)
				}
				committed = next
				offsets = append(offsets, next)
				return nil
			})
			if fail {
				if !errors.Is(err, errAdmission) || calls != 1 || !reflect.DeepEqual(offsets, []int64{41}) {
					t.Fatalf("failed commit: %v calls=%d offsets=%v", err, calls, offsets)
				}
			} else if !errors.Is(err, context.Canceled) || calls != 2 || !reflect.DeepEqual(offsets, []int64{41, 42}) {
				t.Fatalf("poll result: %v calls=%d offsets=%v", err, calls, offsets)
			}
		})
	}
}

func TestPollOutageAndRateLimitUseCancellableBackoff(t *testing.T) {
	for _, rateLimited := range []bool{false, true} {
		t.Run(fmt.Sprint(rateLimited), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				io.Copy(io.Discard, r.Body)
				if rateLimited {
					return response(r, 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":7}}`), nil
				}
				return nil, errors.New("platform unavailable")
			}))
			c.pause = func(ctx context.Context, delay time.Duration) error {
				want := time.Second
				if rateLimited {
					want = 7 * time.Second
				}
				if delay != want {
					t.Fatalf("backoff=%v, want=%v", delay, want)
				}
				cancel()
				return ctx.Err()
			}
			err := c.poll(ctx, 40, func(context.Context, gotgbot.Update, int64) error { t.Fatal("outage admitted input"); return nil })
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("outage=%v calls=%d", err, calls)
			}
		})
	}
}

func TestTokenEchoCannotReachAdmitter(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		io.Copy(io.Discard, r.Body)
		return response(r, 200, `{"ok":true,"result":[{"update_id":40,"message":{"message_id":7,"text":"`+testToken+`"}}]}`), nil
	}))
	err := c.poll(ctx, 40, func(_ context.Context, u gotgbot.Update, next int64) error {
		if u.Message.Text != secret.Placeholder {
			t.Fatal("token echo reached durable admission")
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("poll=%v", err)
	}
}
