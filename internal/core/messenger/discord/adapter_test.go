// SPDX-License-Identifier: Apache-2.0
package discord_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/gateway/discord"
	"github.com/thisisjun786/lina/internal/core/messenger"
	messengerdiscord "github.com/thisisjun786/lina/internal/core/messenger/discord"
	"github.com/thisisjun786/lina/kit/secret"
)

type transportFunc func(*http.Request) (*http.Response, error)

// apiRequest drops the fixed API prefix, so a test matches the platform path.
func apiRequest(r *http.Request) *http.Request {
	if r.URL.Path == "/api/v10" || strings.HasPrefix(r.URL.Path, "/api/v10/") {
		clone := r.Clone(r.Context())
		clone.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v10")
		return clone
	}
	return r
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(r *http.Request, status int, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type fixture struct {
	adapter   *messengerdiscord.Adapter
	db        *canon.Store
	requested []string
	mu        sync.Mutex
}

func newFixture(t *testing.T, route func(*http.Request) *http.Response) *fixture {
	t.Helper()
	f := &fixture{db: fixtureDB(t)}
	f.adapter = mustAdapter(t, f.db, transportFunc(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		f.requested = append(f.requested, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		return route(apiRequest(r)), nil
	}))
	return f
}

// fixtureDB opens a canon store and pairs the bot with the person's numeric
// user id, which is what proves a message is the person's.
func fixtureDB(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 7, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	store := channels.New(db)
	bot := channels.Bot{Platform: "discord", Account: "123456"}
	if err = store.Connect(t.Context(), bot, "person"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := store.PairingCode(t.Context(), bot, "person", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Pair(t.Context(), bot, code, "200", "100", now); err != nil {
		t.Fatal(err)
	}
	return db
}

// goneClient mirrors the composition root's translation of a message the bot
// can never read.
type goneClient struct{ *discord.Client }

func (c goneClient) Get(ctx context.Context, path string, query url.Values, out any) error {
	err := c.Client.Get(ctx, path, query, out)
	if errors.Is(err, discord.ErrNotFound) || errors.Is(err, discord.ErrForbidden) {
		return messengerdiscord.ErrGone
	}
	return err
}

func mustAdapter(t *testing.T, db *canon.Store, tr http.RoundTripper) *messengerdiscord.Adapter {
	t.Helper()
	token := secret.New("synthetic.discord.token")
	client, err := discord.NewClient(token, tr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	a, err := messengerdiscord.New(messengerdiscord.Config{
		Source: messenger.Source{Person: "person", Platform: "discord", Account: "123456", Mode: messenger.ModeOfficial, Adapter: "discord", State: messenger.StateConnected},
		DB:     db, Client: goneClient{client},
		Listen: func(ctx context.Context, handle func(context.Context, string, json.RawMessage) error) error {
			err := discord.Listen(ctx, discord.Listener{
				Bot:   channels.Bot{Platform: "discord", Account: "123456"},
				Token: token, DB: db, Transport: tr, Intents: discord.IntentMessageContent,
				Handle: func(ctx context.Context, kind string, data json.RawMessage, _ string) error {
					return handle(ctx, kind, data)
				},
			})
			if errors.Is(err, discord.ErrIntentsDenied) {
				return messenger.ErrTextUnavailable
			}
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fixture) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requested...)
}

const guilds = `[{"id":"g1","name":"Guild"}]`
const guildChannels = `[{"id":"100","name":"general","type":0},{"id":"300","name":"voice","type":2},{"id":"500","name":"news","type":5}]`
const activeThreads = `{"threads":[{"id":"400","name":"topic","type":11,"parent_id":"100"}]}`
const archivedThreads = `{"threads":[],"has_more":false}`

func history() string {
	return `[
 {"id":"901","channel_id":"100","guild_id":"g1","content":"ship it","type":0,"timestamp":"2026-10-24T08:00:00.000000+00:00",
  "author":{"id":"200","username":"owner","global_name":"Jun","bot":false}},
 {"id":"902","channel_id":"100","guild_id":"g1","content":"build is green","type":0,"timestamp":"2026-10-24T08:01:00.000000+00:00",
  "author":{"id":"500","username":"builder","bot":true}},
 {"id":"903","channel_id":"100","guild_id":"g1","content":"me too","type":0,"timestamp":"2026-10-24T08:02:00.000000+00:00",
  "edited_timestamp":"2026-10-24T08:03:00.000000+00:00","referenced_message":{"id":"901"},
  "attachments":[{"id":"att-1","filename":"log.txt","content_type":"text/plain","size":12,"url":"https://cdn.discordapp.com/attachments/100/att-1/log.txt"}],
  "author":{"id":"600","username":"guest","bot":false}},
 {"id":"904","channel_id":"100","guild_id":"g1","content":"pinned a message","type":6,"timestamp":"2026-10-24T08:04:00.000000+00:00",
  "author":{"id":"700","username":"system","bot":false,"system":true}}
]`
}

func TestListChatsIncludesGuildChannelsAndThreads(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		switch r.URL.Path {
		case "/users/@me/guilds":
			return reply(r, 200, guilds)
		case "/guilds/g1/channels":
			return reply(r, 200, guildChannels)
		case "/guilds/g1/threads/active":
			return reply(r, 200, activeThreads)
		case "/channels/100/threads/archived/public", "/channels/500/threads/archived/public":
			return reply(r, 200, archivedThreads)
		}
		return reply(r, 404, `{"code":10003,"message":"unknown"}`)
	})
	chats, err := f.adapter.ListChats(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, chat := range chats {
		got[chat.ID] = chat.Kind
	}
	want := map[string]string{"100": "channel", "500": "channel", "400": "thread"}
	if len(got) != len(want) {
		t.Fatalf("chats = %v, want %v", got, want)
	}
	for id, kind := range want {
		if got[id] != kind {
			t.Fatalf("chat %s kind = %q, want %q", id, got[id], kind)
		}
	}
	if _, ok := got["300"]; ok {
		t.Fatal("a voice channel became a source chat")
	}
}

func TestFirstFillPagesForwardFromTheOldestMessage(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		switch r.URL.Path {
		case "/users/@me/guilds":
			return reply(r, 200, guilds)
		case "/guilds/g1/channels":
			return reply(r, 200, guildChannels)
		case "/guilds/g1/threads/active":
			return reply(r, 200, activeThreads)
		case "/channels/100/threads/archived/public", "/channels/500/threads/archived/public":
			return reply(r, 200, archivedThreads)
		case "/channels/100/messages":
			if r.URL.Query().Get("after") != "0" || r.URL.Query().Get("limit") != "100" {
				t.Errorf("first fill query = %s", r.URL.RawQuery)
			}
			return reply(r, 200, history())
		}
		return reply(r, 200, `[]`)
	})
	if _, err := f.adapter.ListChats(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.adapter.Page(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(page.Messages))
	}
	m := page.Messages
	if m[0].Route != messenger.RoutePerson || m[0].Author.ID != "200" || m[0].Author.Name != "Jun" || m[0].Text != "ship it" {
		t.Fatalf("person message = %+v", m[0])
	}
	if !m[0].At.Equal(time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("message time = %s", m[0].At)
	}
	if m[1].Route != messenger.RouteAutomation {
		t.Fatalf("bot message route = %q, want automation", m[1].Route)
	}
	if m[2].Route != messenger.RouteParticipant || m[2].ReplyTo != "901" || len(m[2].Attachments) != 1 || m[2].Attachments[0].Name != "log.txt" {
		t.Fatalf("participant message = %+v", m[2])
	}
	if m[2].Edited.IsZero() {
		t.Fatal("edit time was dropped")
	}
	if m[3].Route != messenger.RouteSystem {
		t.Fatalf("system message route = %q, want system", m[3].Route)
	}
	if page.HasMore {
		t.Fatal("a short page still says more")
	}
	if string(page.Next) != `{"after":"904"}` {
		t.Fatalf("cursor = %s", page.Next)
	}
}

func TestIncrementalPageStartsAfterTheCommittedCursor(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		if r.URL.Path == "/channels/100/messages" {
			if got := r.URL.Query().Get("after"); got != "904" {
				t.Errorf("after = %q, want the committed cursor", got)
			}
			return reply(r, 200, `[{"id":"905","channel_id":"100","content":"weekends too","type":0,"author":{"id":"600","username":"guest"}}]`)
		}
		return reply(r, 200, `[]`)
	})
	page, err := f.adapter.Page(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100", messenger.Cursor(`{"after":"904"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "905" {
		t.Fatalf("page = %+v", page)
	}
	if page.Messages[0].Text != "weekends too" || page.Messages[0].Route != messenger.RouteParticipant {
		t.Fatalf("message = %+v", page.Messages[0])
	}
	if string(page.Next) != `{"after":"905"}` {
		t.Fatalf("next = %s", page.Next)
	}
}

func TestRateLimitHonoursRetryAfterAndKeepsTheCursor(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(r *http.Request) *http.Response {
		if r.URL.Path == "/channels/100/messages" {
			if calls.Add(1) == 1 {
				return reply(r, 429, `{"message":"rate limited","retry_after":0.05,"global":false}`)
			}
			if got := r.URL.Query().Get("after"); got != "904" {
				t.Errorf("retry lost the cursor: after = %q", got)
			}
			return reply(r, 200, `[]`)
		}
		return reply(r, 200, `[]`)
	})
	start := time.Now()
	page, err := f.adapter.Page(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100", messenger.Cursor(`{"after":"904"}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("retry fired after %s, before the platform's wait", elapsed)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want the retry", calls.Load())
	}
	if string(page.Next) != `{"after":"904"}` {
		t.Fatalf("next = %s", page.Next)
	}
}

func TestWithoutMessageContentTheSourceSaysTextIsUnavailable(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		if r.URL.Path == "/channels/100/messages" {
			// Without the privileged intent Discord returns empty content and
			// keeps attachment metadata only.
			return reply(r, 200, `[{"id":"901","channel_id":"100","content":"","type":0,"author":{"id":"200","username":"owner"}}]`)
		}
		return reply(r, 200, `[]`)
	})
	_, err := f.adapter.Page(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100", "")
	if !errors.Is(err, messenger.ErrTextUnavailable) {
		t.Fatalf("err = %v, want text unavailable", err)
	}
}

func TestGatewayRefusingTheIntentSaysTextIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var identify struct {
			Op int `json:"op"`
		}
		if wsjson.Read(ctx, conn, &identify) != nil {
			return
		}
		_ = conn.Close(websocket.StatusCode(4014), "disallowed intents")
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	a := mustAdapter(t, fixtureDB(t), transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "discord.com" {
			return reply(r, 200, `[]`), nil
		}
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	}))
	err := a.Live(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}, func(messenger.Event) error { return nil })
	if !errors.Is(err, messenger.ErrTextUnavailable) {
		t.Fatalf("err = %v, want text unavailable", err)
	}
}

func TestLiveMessageEditAndDeleteEvents(t *testing.T) {
	frames := make(chan any, 8)
	identified := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var auth struct {
			Op int `json:"op"`
			D  struct {
				Intents int `json:"intents"`
			} `json:"d"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		if auth.Op != 2 || auth.D.Intents&int(discord.IntentMessageContent) == 0 {
			t.Errorf("identify = %+v, want the content intent", auth)
			return
		}
		identified <- 1
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if wsjson.Write(ctx, conn, frame) != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	a := mustAdapter(t, fixtureDB(t), transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "discord.com" {
			switch apiRequest(r).URL.Path {
			case "/users/@me/guilds":
				return reply(r, 200, guilds), nil
			case "/guilds/g1/channels":
				return reply(r, 200, guildChannels), nil
			case "/guilds/g1/threads/active":
				return reply(r, 200, activeThreads), nil
			}
			return reply(r, 200, `{"threads":[],"has_more":false}`), nil
		}
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	}))
	if _, err := a.ListChats(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan messenger.Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- a.Live(ctx, messenger.SourceID{Platform: "discord", Account: "123456"}, func(e messenger.Event) error { events <- e; return nil })
	}()
	select {
	case <-identified:
	case err := <-done:
		t.Fatalf("live stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("live never identified")
	}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{
		"id": "901", "channel_id": "100", "guild_id": "g1", "content": "ship it", "type": 0,
		"timestamp": "2026-10-24T08:00:00.000000+00:00", "author": map[string]any{"id": "200", "username": "owner"}}}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_UPDATE", "s": 3, "d": map[string]any{
		"id": "901", "channel_id": "100", "guild_id": "g1", "content": "ship it now", "type": 0,
		"timestamp": "2026-10-24T08:00:00.000000+00:00", "edited_timestamp": "2026-10-24T08:05:00.000000+00:00",
		"author": map[string]any{"id": "200", "username": "owner"}}}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_DELETE", "s": 4, "d": map[string]any{"id": "902", "channel_id": "100", "guild_id": "g1"}}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_DELETE_BULK", "s": 5, "d": map[string]any{"ids": []string{"903", "904"}, "channel_id": "100", "guild_id": "g1"}}

	created := <-events
	if created.Message == nil || created.Message.ID != "901" || created.Message.Route != messenger.RoutePerson || created.Chat != "100" {
		t.Fatalf("created = %+v", created)
	}
	updated := <-events
	if updated.Message == nil || updated.Message.ID != "901" || updated.Message.Edited.IsZero() || updated.Message.Text != "ship it now" {
		t.Fatalf("updated = %+v", updated)
	}
	deleted := <-events
	if deleted.Deletion == nil || deleted.Deletion.Message != "902" || deleted.Chat != "100" {
		t.Fatalf("deleted = %+v", deleted)
	}
	for _, want := range []string{"903", "904"} {
		bulk := <-events
		if bulk.Deletion == nil || bulk.Deletion.Message != want {
			t.Fatalf("bulk deletion = %+v, want %s", bulk, want)
		}
	}
	// An event for a chat the source does not read is not delivered.
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 6, "d": map[string]any{
		"id": "910", "channel_id": "999", "content": "elsewhere", "type": 0, "author": map[string]any{"id": "600", "username": "guest"}}}
	select {
	case e := <-events:
		t.Fatalf("unlisted chat delivered: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("live did not stop")
	}
}

func TestMembersPageThroughTheGuild(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		switch r.URL.Path {
		case "/users/@me/guilds":
			return reply(r, 200, guilds)
		case "/guilds/g1/channels":
			return reply(r, 200, guildChannels)
		case "/guilds/g1/threads/active":
			return reply(r, 200, activeThreads)
		case "/channels/100/threads/archived/public", "/channels/500/threads/archived/public":
			return reply(r, 200, archivedThreads)
		}
		if r.URL.Path == "/guilds/g1/members" {
			if r.URL.Query().Get("after") == "" {
				members := make([]string, 1000)
				for i := range members {
					members[i] = fmt.Sprintf(`{"user":{"id":"%d","username":"member"}}`, 100000+i)
				}
				return reply(r, 200, "["+strings.Join(members, ",")+"]")
			}
			return reply(r, 200, `[{"user":{"id":"600","username":"guest"}}]`)
		}
		return reply(r, 200, `[]`)
	})
	members, err := f.adapter.Members(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1001 || members[0].ID != "100000" || members[1000].Name != "guest" {
		t.Fatalf("members = %d", len(members))
	}
}

func TestSignInReportsNeedsSignInOnUnauthorized(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		if r.URL.Path == "/users/@me" {
			return reply(r, 401, `{"code":0,"message":"401: Unauthorized"}`)
		}
		return reply(r, 200, `[]`)
	})
	state, err := f.adapter.SignIn(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	if state != messenger.NeedsSignIn {
		t.Fatalf("state = %q, want needs sign-in", state)
	}
}

func TestCapabilitiesNameThePlatform(t *testing.T) {
	a := mustAdapter(t, fixtureDB(t), transportFunc(func(r *http.Request) (*http.Response, error) { return reply(r, 200, `[]`), nil }))
	caps := a.Capabilities()
	if !caps.Edits || !caps.Deletions || !caps.LiveEvents || caps.HistoryDepth == "" || caps.CursorKind != "snowflake" {
		t.Fatalf("capabilities = %+v", caps)
	}
	if len(caps.Writes) != 0 {
		t.Fatalf("reads-only increment named writes: %v", caps.Writes)
	}
}

// A bound adapter must never read another account through this bot's token.
func TestAdapterRejectsOtherSource(t *testing.T) {
	a := mustAdapter(t, fixtureDB(t), transportFunc(func(r *http.Request) (*http.Response, error) {
		t.Error("mismatched source reached Discord")
		return reply(r, 200, `[]`), nil
	}))
	for _, id := range []messenger.SourceID{
		{Platform: "discord", Account: "654321"},
		{Platform: "teams", Account: "123456"},
	} {
		ctx := t.Context()
		_, signIn := a.SignIn(ctx, id)
		_, chats := a.ListChats(ctx, id)
		_, page := a.Page(ctx, id, "100", "")
		_, members := a.Members(ctx, id, "100")
		live := a.Live(ctx, id, func(messenger.Event) error { t.Error("mismatched source delivered event"); return nil })
		_, attachment := a.FetchAttachment(ctx, id, "100", "200")
		for name, err := range map[string]error{"sign-in": signIn, "chats": chats, "page": page, "members": members, "live": live, "attachment": attachment} {
			if !errors.Is(err, messenger.ErrInvalid) {
				t.Errorf("%s for %+v: %v", name, id, err)
			}
		}
	}
}

func TestArchivedThreadsUseArchiveTimestamp(t *testing.T) {
	calls := 0
	f := newFixture(t, func(r *http.Request) *http.Response {
		switch r.URL.Path {
		case "/users/@me/guilds":
			return reply(r, 200, guilds)
		case "/guilds/g1/channels":
			return reply(r, 200, `[{"id":"100","name":"general","type":0}]`)
		case "/guilds/g1/threads/active":
			return reply(r, 200, archivedThreads)
		case "/channels/100/threads/archived/public":
			calls++
			if calls == 1 {
				return reply(r, 200, `{"threads":[{"id":"900","type":11,"thread_metadata":{"archive_timestamp":"2026-10-23T12:00:00+00:00"}}],"has_more":true}`)
			}
			if r.URL.Query().Get("before") != "2026-10-23T12:00:00+00:00" {
				t.Errorf("archive cursor = %q", r.URL.Query().Get("before"))
				return reply(r, 400, `{}`)
			}
			return reply(r, 200, `{"threads":[{"id":"950","type":11}],"has_more":false}`)
		}
		t.Errorf("unexpected path %s", r.URL.Path)
		return reply(r, 404, `{}`)
	})
	chats, err := f.adapter.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: "123456"})
	if err != nil || len(chats) != 3 || calls != 2 {
		t.Fatalf("chats=%+v calls=%d err=%v", chats, calls, err)
	}
}

func TestPollAndComponentOnlyMessagesHaveAvailableContent(t *testing.T) {
	for _, field := range []string{`"poll":{"question":{"text":"Lunch?"}}`, `"components":[{"type":1}]`} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t, func(r *http.Request) *http.Response {
				return reply(r, 200, `[{"id":"901","channel_id":"100","type":0,"author":{"id":"201"},`+field+`}]`)
			})
			page, err := f.adapter.Page(t.Context(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100", "")
			if err != nil || len(page.Messages) != 1 {
				t.Fatalf("page=%+v err=%v", page, err)
			}
		})
	}
}

func TestThreadStarterMessagesDoNotRequireContent(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		return reply(r, 200, `[{"id":"901","channel_id":"400","type":21,"referenced_message":{"id":"800"}}]`)
	})
	page, err := f.adapter.Page(t.Context(), messenger.SourceID{Platform: "discord", Account: "123456"}, "400", "")
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("thread starter page=%+v err=%v", page, err)
	}
	if page.Next != `{"after":"901"}` || page.Messages[0].ReplyTo != "800" {
		t.Fatalf("thread starter did not advance with its reference: %+v", page)
	}
}

// A forward carries its words and media in message_snapshots, not in content.
// The message is readable, so it must never be reported as withheld text.
func TestForwardedMessagesReadTheirSnapshot(t *testing.T) {
	f := newFixture(t, func(r *http.Request) *http.Response {
		return reply(r, 200, `[{"id":"901","channel_id":"100","type":0,"message_reference":{"type":1},
			"message_snapshots":[{"message":{"content":"the forwarded words","timestamp":"2026-10-23T08:00:00.000000+00:00",
			"attachments":[{"id":"att-9","filename":"plan.txt","content_type":"text/plain","size":42}]}}],
			"author":{"id":"200","username":"owner"}}]`)
	})
	page, err := f.adapter.Page(t.Context(), messenger.SourceID{Platform: "discord", Account: "123456"}, "100", "")
	if err != nil {
		t.Fatalf("a forward must not report withheld text: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("page=%+v", page)
	}
	m := page.Messages[0]
	if m.Text != "the forwarded words" || len(m.Attachments) != 1 || m.Attachments[0].Name != "plan.txt" {
		t.Fatalf("forward snapshot not read: %+v", m)
	}
	if page.Next != `{"after":"901"}` {
		t.Fatalf("forward did not advance the cursor: %q", page.Next)
	}
}

// A forward arriving live carries its words in message_snapshots too. The live
// reader must deliver it instead of failing the shared Gateway session.
func TestLiveForwardedMessageReadsItsSnapshot(t *testing.T) {
	frames := make(chan any, 4)
	identified := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var auth struct {
			Op int `json:"op"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		identified <- 1
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if wsjson.Write(ctx, conn, frame) != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	a := mustAdapter(t, fixtureDB(t), transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "discord.com" {
			switch apiRequest(r).URL.Path {
			case "/users/@me/guilds":
				return reply(r, 200, guilds), nil
			case "/guilds/g1/channels":
				return reply(r, 200, guildChannels), nil
			case "/guilds/g1/threads/active":
				return reply(r, 200, activeThreads), nil
			}
			return reply(r, 200, `{"threads":[],"has_more":false}`), nil
		}
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	}))
	if _, err := a.ListChats(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan messenger.Event, 4)
	done := make(chan error, 1)
	go func() {
		done <- a.Live(ctx, messenger.SourceID{Platform: "discord", Account: "123456"}, func(e messenger.Event) error { events <- e; return nil })
	}()
	select {
	case <-identified:
	case err := <-done:
		t.Fatalf("live stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("live never identified")
	}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{
		"id": "901", "channel_id": "100", "guild_id": "g1", "type": 0,
		"message_reference": map[string]any{"type": 1},
		"message_snapshots": []any{map[string]any{"message": map[string]any{"content": "the forwarded words", "timestamp": "2026-10-23T08:00:00.000000+00:00"}}},
		"timestamp":         "2026-10-24T08:00:00.000000+00:00",
		"author":            map[string]any{"id": "200", "username": "owner"}}}
	select {
	case e := <-events:
		if e.Message == nil || e.Message.Text != "the forwarded words" {
			t.Fatalf("live forward = %+v", e)
		}
	case err := <-done:
		t.Fatalf("the shared session failed on a forward: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the live reader never delivered the forward")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("live did not stop")
	}
}

// gatewayServer starts a synthetic Gateway for one adapter and returns the
// frame channel a test writes dispatches to. It does not start a live reader.
func gatewayServer(t *testing.T, rest func(*http.Request) *http.Response) (chan any, <-chan int, *messengerdiscord.Adapter, func()) {
	t.Helper()
	frames := make(chan any, 8)
	identified := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var auth struct {
			Op int `json:"op"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		identified <- 1
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if wsjson.Write(ctx, conn, frame) != nil {
					return
				}
			}
		}
	}))
	endpoint, _ := url.Parse(server.URL)
	a := mustAdapter(t, fixtureDB(t), transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "discord.com" {
			switch apiRequest(r).URL.Path {
			case "/users/@me/guilds":
				return reply(r, 200, guilds), nil
			case "/guilds/g1/channels":
				return reply(r, 200, guildChannels), nil
			case "/guilds/g1/threads/active":
				return reply(r, 200, activeThreads), nil
			}
			if strings.HasSuffix(apiRequest(r).URL.Path, "/threads/archived/public") {
				return reply(r, 200, `{"threads":[],"has_more":false}`), nil
			}
			return rest(apiRequest(r)), nil
		}
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	}))
	if _, err := a.ListChats(context.Background(), messenger.SourceID{Platform: "discord", Account: "123456"}); err != nil {
		t.Fatal(err)
	}
	return frames, identified, a, func() { server.Close() }
}

// gatewayFixture starts a live reader on a synthetic Gateway and returns the
// frame channel plus the events it delivered.
func gatewayFixture(t *testing.T, rest func(*http.Request) *http.Response) (chan any, chan messenger.Event, *messengerdiscord.Adapter, func()) {
	t.Helper()
	frames, joined, a, stop := gatewayServer(t, rest)
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan messenger.Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- a.Live(ctx, messenger.SourceID{Platform: "discord", Account: "123456"}, func(e messenger.Event) error { events <- e; return nil })
	}()
	<-joined
	return frames, events, a, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("live stopped with %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("live did not stop")
		}
		stop()
	}
}

// Discord sends a partial MESSAGE_UPDATE. Only the fields it sent may reach the
// record: an update with no author is completed from REST, an embed-only update
// is not a text edit, and an update whose text is unavailable never stops the
// shared session.
func TestPartialMessageUpdateReadsTheStoredMessage(t *testing.T) {
	frames, events, _, stop := gatewayFixture(t, func(r *http.Request) *http.Response {
		if r.URL.Path == "/channels/100/messages/901" {
			return reply(r, 200, `{"id":"901","channel_id":"100","guild_id":"g1","content":"edited words","type":0,
				"edited_timestamp":"2026-10-24T08:05:00.000000+00:00","timestamp":"2026-10-24T08:00:00.000000+00:00",
				"author":{"id":"200","username":"owner"}}`)
		}
		if r.URL.Path == "/channels/100/messages/902" {
			return reply(r, 200, `{"id":"902","channel_id":"100","guild_id":"g1","content":"","type":0,
				"edited_timestamp":"2026-10-24T08:06:00.000000+00:00","timestamp":"2026-10-24T08:00:00.000000+00:00",
				"author":{"id":"200","username":"owner"}}`)
		}
		return reply(r, 404, `{"code":10003,"message":"unknown"}`)
	})
	defer stop()
	// A partial update names no author, so the full message is read from REST.
	frames <- map[string]any{"op": 0, "t": "MESSAGE_UPDATE", "s": 2, "d": map[string]any{
		"id": "901", "channel_id": "100", "guild_id": "g1", "content": "edited words",
		"edited_timestamp": "2026-10-24T08:05:00.000000+00:00"}}
	select {
	case e := <-events:
		if e.Message == nil || e.Message.Text != "edited words" || e.Message.Author.ID != "200" || e.Message.Route != messenger.RoutePerson {
			t.Fatalf("partial update = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the partial update was not delivered")
	}
	// An embed-only update carries neither an edit time nor a content change.
	frames <- map[string]any{"op": 0, "t": "MESSAGE_UPDATE", "s": 3, "d": map[string]any{
		"id": "901", "channel_id": "100", "guild_id": "g1", "embeds": []any{map[string]any{"type": "rich"}}}}
	// An update whose text the platform withholds must not fail the session.
	frames <- map[string]any{"op": 0, "t": "MESSAGE_UPDATE", "s": 4, "d": map[string]any{
		"id": "902", "channel_id": "100", "guild_id": "g1", "edited_timestamp": "2026-10-24T08:06:00.000000+00:00"}}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 5, "d": map[string]any{
		"id": "903", "channel_id": "100", "guild_id": "g1", "content": "still here", "type": 0,
		"timestamp": "2026-10-24T08:07:00.000000+00:00", "author": map[string]any{"id": "200", "username": "owner"}}}
	for {
		select {
		case e := <-events:
			if e.Message == nil || e.Message.ID != "903" {
				t.Fatalf("skipped update was delivered: %+v", e)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("the session stopped after an update without text")
		}
	}
}

// A partial update needs the full message from REST. A transient failure must
// keep the dispatch so the session replays it; only an answer that will not
// change, such as a deleted message, is skipped.
func TestUpdateHydrationRetriesTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		repeats bool
	}{
		{"transient", 503, `{"message":"try later"}`, true},
		{"gone", 404, `{"code":10008,"message":"Unknown Message"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fetches atomic.Int32
			frames, events, _, stop := gatewayFixture(t, func(r *http.Request) *http.Response {
				fetches.Add(1)
				return reply(r, tc.status, tc.body)
			})
			defer stop()
			frames <- map[string]any{"op": 0, "t": "MESSAGE_UPDATE", "s": 2, "d": map[string]any{
				"id": "901", "channel_id": "100", "guild_id": "g1", "content": "edited words",
				"edited_timestamp": "2026-10-24T08:05:00.000000+00:00"}}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && fetches.Load() < 3 {
				time.Sleep(20 * time.Millisecond)
			}
			if tc.repeats {
				if fetches.Load() < 2 {
					t.Fatalf("a transient hydration failure was acknowledged: fetches=%d", fetches.Load())
				}
			} else if fetches.Load() != 1 {
				t.Fatalf("a gone message was fetched %d times", fetches.Load())
			}
			select {
			case e := <-events:
				t.Fatalf("an unreadable update was admitted: %+v", e)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}
