// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

func readForm(t *testing.T, r *http.Request) {
	t.Helper()
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.MultipartForm.RemoveAll() })
}

func TestMarkdownUsesTelegramEntitiesAndPreservesCode(t *testing.T) {
	f := newAdapterFixture(t)
	input := "# Status\n\n😀 **ready** and *steady*, ~~old~~. [result](https://example.invalid/a_(b))\n\n```go\nif a < b { fmt.Println(\"**literal**\") }\n```\n\n`x_y` <raw> & text"
	calls := 0
	a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		readForm(t, r)
		if r.FormValue("chat_id") != "100" || r.FormValue("message_thread_id") != "3" {
			t.Fatal("changed destination")
		}
		text := r.FormValue("text")
		var entities []gotgbot.MessageEntity
		if err := json.Unmarshal([]byte(r.FormValue("entities")), &entities); err != nil {
			t.Fatalf("no Telegram formatting: %v", err)
		}
		units := utf16.Encode([]rune(text))
		found := map[string]string{}
		for _, e := range entities {
			if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > int64(len(units)) {
				t.Fatalf("bad UTF-16 entity: %+v", e)
			}
			value := string(utf16.Decode(units[e.Offset : e.Offset+e.Length]))
			found[e.Type+":"+value] = e.Url + e.Language
		}
		for _, key := range []string{"bold:Status", "bold:ready", "italic:steady", "strikethrough:old", "code:x_y"} {
			if _, ok := found[key]; !ok {
				t.Errorf("missing %s in %+v", key, found)
			}
		}
		if found["text_link:result"] != "https://example.invalid/a_(b)" || found["pre:if a < b { fmt.Println(\"**literal**\") }\n"] != "go" {
			t.Fatalf("link or code changed: %+v", found)
		}
		if !strings.Contains(text, "<raw> & text") {
			t.Fatal("literal markup lost")
		}
		return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
	}))
	address := f.address
	address.Topic = "3"
	l := lease.New(func() {})
	defer l.Release()
	result, err := a.Send(t.Context(), l, gateway.Outbound{Address: address, Text: input})
	if err != nil || result.Outcome != gateway.Delivered || calls != 1 {
		t.Fatalf("%+v %v calls=%d", result, err, calls)
	}
}

func TestFormattingFallbackOnlyAfterDefiniteEntityRejection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        int
		description string
		wantCalls   int
		outcome     gateway.Outcome
	}{
		{"entities", 400, "Bad Request: can't parse entities", 2, gateway.Delivered},
		{"unknown", 500, "can't parse entities", 1, gateway.Uncertain},
		{"other refusal", 400, "chat not found", 1, gateway.Uncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdapterFixture(t)
			calls := 0
			a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				readForm(t, r)
				if r.FormValue("chat_id") != "100" {
					t.Fatal("fallback changed chat")
				}
				if calls == 1 {
					return response(r, tc.code, fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, tc.code, tc.description)), nil
				}
				if r.FormValue("text") != "**hello**" || r.FormValue("entities") != "" || r.FormValue("parse_mode") != "" {
					t.Fatal("fallback was not original plain text")
				}
				return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
			}))
			l := lease.New(func() {})
			defer l.Release()
			result, err := a.Send(t.Context(), l, gateway.Outbound{Address: f.address, Text: "**hello**"})
			if err != nil || result.Outcome != tc.outcome || calls != tc.wantCalls {
				t.Fatalf("%+v %v calls=%d", result, err, calls)
			}
		})
	}
}

func TestShapeReopensCodeFencesWithoutLosingWhitespace(t *testing.T) {
	for _, marker := range []string{"```", "~~~~"} {
		body := strings.Repeat("  😀 **literal**  \n", 400)
		parts, err := Shape(marker+"go\n"+body+marker, "", "")
		if err != nil || len(parts) < 2 || len(parts) > 3 {
			t.Fatalf("parts=%d err=%v", len(parts), err)
		}
		var recovered strings.Builder
		for _, part := range parts {
			if !textFits(part, 4096) || !strings.HasPrefix(part, marker+"go\n") || !strings.HasSuffix(part, marker) {
				t.Fatalf("broken code fence in %q", part)
			}
			recovered.WriteString(strings.TrimSuffix(strings.TrimPrefix(part, marker+"go\n"), marker))
		}
		if recovered.String() != body {
			t.Fatal("split changed code whitespace or content")
		}
	}
}

func TestFloodWaitIsNotShortenedAndCoversEdits(t *testing.T) {
	calls := 0
	now := time.Unix(1000, 0)
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
		}
		return response(r, 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":90000}}`), nil
	}))
	c.now = func() time.Time { return now }
	_, err := c.sendMessage(t.Context(), 12, 3, "first", nil)
	var f *failure
	if !errors.As(err, &f) || f.retryAfter != 90000*time.Second {
		t.Fatalf("flood wait shortened: %v %+v", err, f)
	}
	err = c.editMessage(t.Context(), 12, 19, "replacement", nil)
	if !errors.As(err, &f) || f.class != "retry_later" || f.retryAfter != 90000*time.Second || calls != 1 {
		t.Fatalf("edit ignored flood wait: %+v calls=%d", f, calls)
	}
	now = now.Add(90000 * time.Second)
	if err := c.editMessage(t.Context(), 12, 19, "replacement", nil); err != nil || calls != 2 {
		t.Fatalf("edit did not resume at deadline: %v calls=%d", err, calls)
	}
}

type admitFunc func(context.Context, gateway.Inbound) error

func (f admitFunc) Admit(ctx context.Context, in gateway.Inbound) error { return f(ctx, in) }

func TestLoopGuardRejectsBotsSelfEchoAndAnonymousSenders(t *testing.T) {
	f := newAdapterFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls, admitted := 0, 0
	a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		readForm(t, r)
		if calls > 1 {
			cancel()
			return nil, ctx.Err()
		}
		return response(r, 200, `{"ok":true,"result":[
		{"update_id":10,"message":{"message_id":10,"chat":{"id":100,"type":"private"},"from":{"id":100,"is_bot":true},"text":"bot"}},
		{"update_id":11,"message":{"message_id":11,"chat":{"id":100,"type":"private"},"from":{"id":123456},"text":"self echo"}},
		{"update_id":12,"message":{"message_id":12,"chat":{"id":100,"type":"private"},"from":{"id":100},"sender_chat":{"id":-100,"type":"channel"},"text":"anonymous"}},
		{"update_id":13,"message":{"message_id":13,"chat":{"id":100,"type":"private"},"from":{"id":100},"text":"human"}}
		]}`), nil
	}))
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { admitted++; return nil })
	err := a.Run(ctx, admitFunc(func(ctx context.Context, in gateway.Inbound) error {
		if in.NativeID != "13" {
			t.Fatalf("loop input reached gateway: %s", in.NativeID)
		}
		return g.Admit(ctx, in)
	}))
	if !errors.Is(err, context.Canceled) || admitted != 1 {
		t.Fatalf("loop guard: %v admitted=%d", err, admitted)
	}
	if cursor, err := f.channels.Cursor(t.Context(), f.bot); err != nil || cursor != "14" {
		t.Fatalf("ignored input stalled cursor: %q %v", cursor, err)
	}
}

func TestEditAlreadyCurrentIsSuccessAndNeverSendsNewMessage(t *testing.T) {
	for _, tc := range []struct {
		code        int
		description string
		success     bool
	}{
		{400, "Bad Request: message is not modified", true},
		{500, "message is not modified", false},
	} {
		calls := 0
		c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			readForm(t, r)
			if !strings.HasSuffix(r.URL.Path, "/editMessageText") || r.FormValue("message_id") != "19" || r.FormValue("chat_id") != "12" {
				t.Fatal("edit changed destination or sent a new message")
			}
			return response(r, tc.code, fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, tc.code, tc.description)), nil
		}))
		err := c.editMessage(t.Context(), 12, 19, "same", nil)
		if (err == nil) != tc.success || calls != 1 {
			t.Fatalf("edit=%v calls=%d", err, calls)
		}
	}
}

func TestPollingConflictHasBoundedRecoveryWithoutDiscardingUpdates(t *testing.T) {
	calls, pauses := 0, 0
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		readForm(t, r)
		if r.FormValue("offset") != "40" || r.FormValue("drop_pending_updates") != "" {
			t.Fatal("conflict discarded updates")
		}
		return response(r, 409, `{"ok":false,"error_code":409,"description":"Conflict: another getUpdates request"}`), nil
	}))
	c.pause = func(_ context.Context, d time.Duration) error {
		pauses++
		if pauses > 5 {
			return errors.New("unbounded conflict recovery")
		}
		if d < 20*time.Second {
			t.Errorf("conflict retry too soon: %v", d)
		}
		return nil
	}
	err := c.poll(t.Context(), 40, func(context.Context, gotgbot.Update, int64) error { t.Fatal("admitted during conflict"); return nil })
	var f *failure
	if !errors.As(err, &f) || f.class != "conflict" || calls != 5 || pauses != 4 {
		t.Fatalf("conflict=%v calls=%d pauses=%d", err, calls, pauses)
	}
}

func TestFormattedEditsKeepMessageIdentityDuringFallback(t *testing.T) {
	f := newAdapterFixture(t)
	calls := 0
	a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		readForm(t, r)
		if !strings.HasSuffix(r.URL.Path, "/editMessageText") || r.FormValue("chat_id") != "100" || r.FormValue("message_id") != "19" {
			t.Fatal("edit rerouted or sent new message")
		}
		if calls == 1 {
			if r.FormValue("entities") == "" {
				t.Fatal("edit lost formatting")
			}
			return response(r, 400, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`), nil
		}
		if r.FormValue("entities") != "" || r.FormValue("text") != "**final**" {
			t.Fatal("incorrect edit fallback")
		}
		return response(r, 400, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`), nil
	}))
	err := a.Edit(t.Context(), gateway.MsgRef{Address: f.address, ID: "19"}, gateway.Outbound{Address: f.address, Text: "**final**"})
	if err != nil || calls != 2 || a.Caps().Streaming {
		t.Fatalf("edit=%v calls=%d streaming=%v", err, calls, a.Caps().Streaming)
	}
}

func TestPollBackoffResetsAndServerFloodWaitWins(t *testing.T) {
	calls := 0
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 3:
			return response(r, 200, `{"ok":true,"result":[]}`), nil
		case 5:
			return response(r, 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":90000}}`), nil
		case 6:
			return response(r, 401, `{"ok":false,"error_code":401}`), nil
		default:
			return nil, errors.New("network unavailable")
		}
	}))
	c.jitter = func(d time.Duration) time.Duration { return d / 4 }
	var waits []time.Duration
	c.pause = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	err := c.poll(t.Context(), 40, func(context.Context, gotgbot.Update, int64) error { t.Fatal("empty poll admitted input"); return nil })
	var f *failure
	if !errors.As(err, &f) || f.class != "unauthorized" {
		t.Fatalf("poll=%v", err)
	}
	want := []time.Duration{1250 * time.Millisecond, 2500 * time.Millisecond, 1250 * time.Millisecond, 90000 * time.Second}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits=%v want=%v", waits, want)
	}
}

func TestWriteCancellationWhileAnotherWriteIsInFlight(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
	}))
	done := make(chan error, 1)
	go func() { _, err := c.sendMessage(t.Context(), 12, 0, "first", nil); done <- err }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := c.editMessage(ctx, 12, 19, "second", nil)
	close(release)
	first := <-done
	if !errors.Is(err, context.Canceled) || first != nil {
		t.Fatalf("cancel=%v first=%v", err, first)
	}
}

func TestFloodWaitOverflowNeverBecomesEarlyRetry(t *testing.T) {
	c, _ := newClient(secret.New(testToken), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(r, 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":9223372036854775807}}`), nil
	}))
	id, err := c.sendMessage(t.Context(), 12, 0, "hello", nil)
	result, err := sendResult(id, err)
	if err != nil || result.Outcome != gateway.Uncertain {
		t.Fatalf("overflow retried early: %+v %v", result, err)
	}
}
