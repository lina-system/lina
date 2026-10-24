// SPDX-License-Identifier: Apache-2.0

// Package webex reads the person's Webex spaces through the public REST API
// with an OAuth integration the person creates. It is one messenger adapter of
// internal/core/messenger: it reports its sign-in state, lists the person's
// rooms, pages a room's messages from a cursor and fetches one attachment.
//
// The source is the person's own account, reached as the documented REST API
// documents it. The adapter never extracts a browser token or a cookie, never
// signs in with the web client's password flow, and never calls the internal
// conversation service. Webex has no maintained Go client library that LINA may
// link (the Webex Go SDK is MPL-2.0), so this package speaks plain HTTPS with
// the standard library.
//
// Reading never marks a message read and never shows presence: the adapter
// sends only GET requests to the API, plus the token endpoint of the sign-in.
package webex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

// Platform is the registry name of this adapter's platform.
const Platform = "webex"

// DefaultBaseURL is Webex's public REST origin.
const DefaultBaseURL = "https://webexapis.com"

const (
	// scopes read the person's own rooms, their messages, the people in them
	// and the person's own profile. The person's integration must name the same
	// scopes; nothing here writes.
	scopes = "spark:rooms_read spark:messages_read spark:memberships_read spark:people_read"
	// redirectPath is the loopback path the client relay listens on. The Webex
	// integration registers the same path with the port the client reports.
	redirectPath = "/auth/callback"
	// defaultPageSize is the room's and the message list's own maximum.
	defaultPageSize = 100
	// refreshLead is how long before expiry the access token is replaced.
	refreshLead = 5 * time.Minute
	// maxRetryWait bounds one rate-limit wait. A longer wait ends this read and
	// the next scheduled read continues from the committed cursor.
	maxRetryWait = 60 * time.Second
	// maxAttempts bounds the retries of one request.
	maxAttempts = 3
	// maxAttachment bounds one attachment download.
	maxAttachment = 32 << 20
)

// ErrMisconfigured reports a request the platform cannot serve as asked.
var ErrMisconfigured = errors.New("webex_misconfigured")

// StatusError is a refusal by the Webex API.
type StatusError struct {
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("webex status %d", e.Status)
}

// RetryError reports a rate limit whose wait is longer than one read may hold.
type RetryError struct {
	After time.Duration
}

func (e *RetryError) Error() string {
	return "webex rate limited for " + e.After.String()
}

// Account is the Webex person the tokens belong to.
type Account struct {
	ID    string
	Email string
	Name  string
}

// Config composes one adapter for one person.
type Config struct {
	// Person is the owner of the credentials and of the source.
	Person string
	// Account pins a previously connected platform identity.
	Account string
	// Vault is Core's secret store. Only Core reads it.
	Vault *secrets.Store
	// BaseURL overrides Webex's origin in tests. Production leaves it empty.
	BaseURL string
	// Transport overrides the HTTP transport in tests. Production leaves it
	// empty, and the adapter uses its own client with a bounded timeout.
	Transport http.RoundTripper
	// Now and Sleep are the clock and the waiter. Tests supply deterministic
	// ones; production uses the elapsed time.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// PageSize bounds one page of rooms or messages.
	PageSize int
	// Interval overrides the schedule this source is read on. Tests set it;
	// production leaves it empty and the adapter's own interval applies.
	Interval time.Duration
}

// Adapter reads one person's Webex account.
type Adapter struct {
	expected string
	person   string
	vault    *secrets.Store
	base     string
	client   *http.Client
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	pageSize int
	interval time.Duration

	mu      sync.Mutex
	pending *attempt
	// refreshMu serializes token replacement so one rotation is spent once.
	refreshMu sync.Mutex
	account   Account
	haveMe    bool
}

type attempt struct {
	Verifier, State, Redirect string
	Expires                   time.Time
}

// New composes the adapter. It reads no credential and contacts nothing.
func New(cfg Config) (*Adapter, error) {
	if cfg.Person == "" || cfg.Vault == nil {
		return nil, ErrMisconfigured
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	origin, err := url.Parse(base)
	if err != nil || origin.Host == "" || (origin.Scheme != "https" && origin.Scheme != "http") {
		return nil, ErrMisconfigured
	}
	pageSize := cfg.PageSize
	if pageSize <= 0 || pageSize > defaultPageSize {
		pageSize = defaultPageSize
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = wait
	}
	return &Adapter{
		person:   cfg.Person,
		expected: cfg.Account,
		vault:    cfg.Vault,
		base:     base,
		client: &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
			// A redirect must never carry the bearer to another origin.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:      now,
		sleep:    sleep,
		pageSize: pageSize,
		interval: cfg.Interval,
	}, nil
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// TokenKey is where the person's Webex tokens live in Core's secret store.
func TokenKey(person string) secrets.Key {
	return secrets.Key{Person: person, Service: Platform, Name: "tokens"}
}

// ClientKey is where the person's own integration's client secret lives.
func ClientKey(person string) secrets.Key {
	return secrets.Key{Person: person, Service: Platform, Name: "client_secret"}
}

// ClientIDKey is where the same integration's public client id lives.
func ClientIDKey(person string) secrets.Key {
	return secrets.Key{Person: person, Service: Platform, Name: "client_id"}
}

// SignIn reports whether the source can read. It replaces the access token when
// the recorded expiry is close, so a dead refresh token shows as needs sign-in
// before any read fails.
func (a *Adapter) SignIn(ctx context.Context, source messenger.SourceID) (messenger.SignInState, error) {
	if err := a.checkSource(ctx, source); err != nil {
		if errors.Is(err, messenger.ErrNeedsSignIn) {
			return messenger.NeedsSignIn, nil
		}
		return messenger.Failing, err
	}
	if _, err := a.access(ctx); err != nil {
		if errors.Is(err, messenger.ErrNeedsSignIn) {
			return messenger.NeedsSignIn, nil
		}
		return messenger.Failing, err
	}
	return messenger.SignedIn, nil
}

// ListChats lists the person's rooms, oldest activity last, following the
// platform's Link header across pages.
func (a *Adapter) ListChats(ctx context.Context, source messenger.SourceID) ([]messenger.Chat, error) {
	if err := a.checkSource(ctx, source); err != nil {
		return nil, err
	}
	var chats []messenger.Chat
	next := a.base + "/v1/rooms?max=" + strconv.Itoa(a.pageSize)
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			return nil, ErrMisconfigured
		}
		seen[next] = true
		var page roomsPage
		link, err := a.get(ctx, next, &page)
		if err != nil {
			return nil, err
		}
		for _, room := range page.Items {
			chats = append(chats, messenger.Chat{ID: room.ID, Name: room.Title, Kind: room.Kind()})
		}
		next, err = nextLink(link, next)
		if err != nil {
			return nil, err
		}
	}
	return chats, nil
}

// Page reads one page of a room oldest first. Cursors walk backwards through
// the room's history with beforeMessage, then stay on the newest page so later
// messages and edits arrive; a finished backfill hands over to that newest page.
func (a *Adapter) Page(ctx context.Context, source messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if err := a.checkSource(ctx, source); err != nil {
		return messenger.Page{}, err
	}
	if chat == "" {
		return messenger.Page{}, messenger.ErrInvalid
	}
	at, err := parseCursor(cursor)
	if err != nil {
		return messenger.Page{}, err
	}
	query := url.Values{"roomId": {chat}, "max": {strconv.Itoa(a.pageSize)}}
	if at.Before != "" {
		query.Set("beforeMessage", at.Before)
	}
	var list listPage
	target := a.base + "/v1/messages?" + query.Encode()
	link, err := a.get(ctx, target, &list)
	if err != nil {
		return messenger.Page{}, err
	}
	next, err := nextLink(link, target)
	if err != nil {
		return messenger.Page{}, err
	}
	more := next != "" || len(list.Items) >= a.pageSize
	if len(list.Items) == 0 {
		if next != "" {
			return messenger.Page{}, ErrMisconfigured
		}
		if at.Mode == modeTail {
			// Nothing newer: the read position stays where it was.
			return messenger.Page{Next: cursor}, nil
		}
		// The room's beginning, or the message a catch-up walked back past.
		return messenger.Page{Next: tailCursor(at.Newest)}, nil
	}
	messages, err := a.messages(ctx, chat, list.Items)
	if err != nil {
		return messenger.Page{}, err
	}
	// The platform returns newest first; a page of a chat is oldest first.
	reverse(messages)
	oldest := list.Items[len(list.Items)-1].ID
	if at.Mode == modeTail {
		if more && at.Newest != "" && !containsID(list.Items, at.Newest) {
			// More arrived than one page holds: walk back to what was already
			// read before moving the tail forward.
			return messenger.Page{Messages: messages, Next: fillCursor(oldest, list.Items[0].ID, at.Newest), HasMore: true}, nil
		}
		return messenger.Page{Messages: messages, Next: tailCursor(list.Items[0].ID)}, nil
	}
	// A catch-up stops at the message the last read ended on: nothing at or
	// older than it is new.
	if at.Stop != "" {
		for i, message := range messages {
			if message.ID == at.Stop {
				return messenger.Page{Messages: messages[i+1:], Next: tailCursor(at.Newest)}, nil
			}
		}
	}
	newest := at.Newest
	if newest == "" {
		newest = list.Items[0].ID
	}
	if !more {
		// The room's first read reached its beginning.
		return messenger.Page{Messages: messages, Next: tailCursor(newest)}, nil
	}
	return messenger.Page{Messages: messages, Next: fillCursor(oldest, newest, at.Stop), HasMore: true}, nil
}

// Members lists the people in a room.
func (a *Adapter) Members(ctx context.Context, source messenger.SourceID, chat string) ([]messenger.Author, error) {
	if err := a.checkSource(ctx, source); err != nil {
		return nil, err
	}
	if chat == "" {
		return nil, messenger.ErrInvalid
	}
	var members []messenger.Author
	next := a.base + "/v1/memberships?max=" + strconv.Itoa(a.pageSize) + "&roomId=" + url.QueryEscape(chat)
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			return nil, ErrMisconfigured
		}
		seen[next] = true
		var page membershipsPage
		link, err := a.get(ctx, next, &page)
		if err != nil {
			return nil, err
		}
		for _, member := range page.Items {
			members = append(members, messenger.Author{ID: member.PersonID, Name: member.Name(), Email: member.PersonEmail})
		}
		next, err = nextLink(link, next)
		if err != nil {
			return nil, err
		}
	}
	return members, nil
}

// Live reports no events: a person's Webex token has no push channel that Core
// may hold open, so the source is read on its schedule.
func (a *Adapter) Live(ctx context.Context, source messenger.SourceID, _ func(messenger.Event) error) error {
	return a.checkSource(ctx, source)
}

// FetchAttachment reads one attachment the room's messages named. The URL must
// be on the API origin, so a message can never send the bearer elsewhere.
func (a *Adapter) FetchAttachment(ctx context.Context, source messenger.SourceID, chat, id string) (io.ReadCloser, error) {
	if err := a.checkSource(ctx, source); err != nil {
		return nil, err
	}
	if chat == "" {
		return nil, messenger.ErrInvalid
	}
	if err := a.sameOrigin(id); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, messenger.ErrInvalid
	}
	response, err := a.send(ctx, request, true)
	if err != nil {
		return nil, err
	}
	return &bounded{Reader: io.LimitReader(response.Body, maxAttachment), closer: response.Body}, nil
}

// bounded closes the response when the reader is done.
type bounded struct {
	io.Reader
	closer io.Closer
}

func (b *bounded) Close() error { return b.closer.Close() }

// Capabilities describes what this platform offers. Webex keeps a room's
// history behind beforeMessage; an edit carries its own updated time. The
// platform reports no deletions, so absence is never read as one.
func (a *Adapter) Capabilities() messenger.Capabilities {
	interval := 5 * time.Minute
	if a.interval > 0 {
		interval = a.interval
	}
	return messenger.Capabilities{
		HistoryDepth: "full",
		CursorKind:   "beforeMessage",
		Edits:        true,
		Deletions:    false,
		LiveEvents:   false,
		Interval:     interval,
		ReadWindow:   0,
	}
}

func (a *Adapter) sameOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return messenger.ErrInvalid
	}
	base, err := url.Parse(a.base)
	if err != nil || !strings.EqualFold(parsed.Scheme, base.Scheme) || !strings.EqualFold(parsed.Host, base.Host) {
		return ErrMisconfigured
	}
	return nil
}

// messages converts the platform's records into messenger messages. The route
// comes from the author id against the connected account, never from a
// display name or the text.
func (a *Adapter) messages(ctx context.Context, chat string, items []wireMessage) ([]messenger.Message, error) {
	account, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]messenger.Message, 0, len(items))
	for _, item := range items {
		if item.ID == "" || item.PersonID == "" || item.Created == "" || item.RoomID != "" && item.RoomID != chat {
			return nil, ErrMisconfigured
		}
		at, err := parseTime(item.Created)
		if err != nil {
			return nil, err
		}
		message := messenger.Message{
			ID:      item.ID,
			Author:  messenger.Author{ID: item.PersonID, Name: item.PersonEmail, Email: item.PersonEmail},
			Route:   messenger.RouteParticipant,
			At:      at,
			Text:    item.Body(),
			ReplyTo: item.ParentID,
		}
		if account.ID != "" && item.PersonID == account.ID {
			message.Route = messenger.RoutePerson
		}
		if item.Updated != "" {
			edited, err := parseTime(item.Updated)
			if err != nil {
				return nil, err
			}
			message.Edited = edited
		}
		for _, file := range item.Files {
			message.Attachments = append(message.Attachments, messenger.Attachment{ID: file, Name: attachmentName(file)})
		}
		out = append(out, message)
	}
	return out, nil
}

// me reads and caches the connected account.
func (a *Adapter) me(ctx context.Context) (Account, error) {
	a.mu.Lock()
	account, known := a.account, a.haveMe
	a.mu.Unlock()
	if known {
		return account, nil
	}
	var person wirePerson
	if _, err := a.get(ctx, a.base+"/v1/people/me", &person); err != nil {
		return Account{}, err
	}
	if person.ID == "" {
		return Account{}, ErrMisconfigured
	}
	account = Account{ID: person.ID, Name: person.DisplayName}
	if len(person.Emails) > 0 {
		account.Email = person.Emails[0]
	}
	a.mu.Lock()
	a.account, a.haveMe = account, true
	a.mu.Unlock()
	return account, nil
}

// get performs one authorized read and decodes the body into out. It returns
// the response's Link header for a paginated list.
func (a *Adapter) get(ctx context.Context, rawurl string, out any) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return "", messenger.ErrInvalid
	}
	response, err := a.send(ctx, request, true)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if out == nil {
		return response.Header.Get("Link"), nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return "", fmt.Errorf("webex response shape: %w", err)
	}
	return response.Header.Get("Link"), nil
}

// send adds the bearer, refreshes once on a rejected token, and waits out a
// rate limit the platform asks for. A refresh that the platform refuses marks
// the source needs sign-in; nothing is retried with another credential.
func (a *Adapter) send(ctx context.Context, request *http.Request, retryable bool) (*http.Response, error) {
	if err := a.sameOrigin(request.URL.String()); err != nil {
		return nil, err
	}
	var last error
	refreshed := false
	for range maxAttempts {
		tokens, err := a.access(ctx)
		if err != nil {
			return nil, err
		}
		outgoing := request.Clone(ctx)
		if outgoing.Body != nil {
			return nil, ErrMisconfigured
		}
		if err := secret.InjectHeader(outgoing.Header, "Authorization", "Bearer ", tokens); err != nil {
			return nil, err
		}
		response, err := a.client.Do(outgoing)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("webex request unavailable")
		}
		switch {
		case response.StatusCode >= 200 && response.StatusCode < 300:
			return response, nil
		case response.StatusCode == http.StatusUnauthorized && retryable && !refreshed:
			refreshed = true
			drain(response)
			if _, err := a.refresh(ctx, true); err != nil {
				return nil, err
			}
			last = &StatusError{Status: http.StatusUnauthorized}
			continue
		case response.StatusCode == http.StatusUnauthorized:
			drain(response)
			if err := a.vault.Delete(TokenKey(a.person)); err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return nil, err
			}
			return nil, messenger.ErrNeedsSignIn
		case (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusLocked) && retryable:
			after := retryAfter(response.Header, a.now())
			drain(response)
			if after > maxRetryWait {
				return nil, &RetryError{After: after}
			}
			if err := a.sleep(ctx, after); err != nil {
				return nil, err
			}
			last = &RetryError{After: after}
			continue
		default:
			drain(response)
			return nil, &StatusError{Status: response.StatusCode}
		}
	}
	if last == nil {
		last = &StatusError{Status: http.StatusTooManyRequests}
	}
	return nil, last
}

func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	response.Body.Close()
}

// retryAfter reads the platform's wait. Webex states whole seconds; an HTTP
// date is accepted too, and a missing value falls back to one second.
func retryAfter(header http.Header, now time.Time) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return time.Second
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if wait := at.Sub(now); wait > 0 {
			return wait
		}
		return 0
	}
	return time.Second
}

// nextLink reads the Link header's next page. It keeps the API origin, so a
// response can never move the bearer to another host.
func nextLink(header, current string) (string, error) {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		next := false
		for _, field := range fields[1:] {
			if strings.TrimSpace(field) == `rel="next"` {
				next = true
			}
		}
		if !next {
			continue
		}
		target := strings.TrimSpace(fields[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			return "", ErrMisconfigured
		}
		parsed, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"))
		base, baseErr := url.Parse(current)
		if err != nil || baseErr != nil || parsed.User != nil || parsed.Fragment != "" || !strings.EqualFold(parsed.Host, base.Host) || parsed.Scheme != base.Scheme || parsed.Path != base.Path || parsed.Query().Get("roomId") != base.Query().Get("roomId") {
			return "", ErrMisconfigured
		}
		return parsed.String(), nil
	}
	return "", nil
}

func containsID(items []wireMessage, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func reverse(list []messenger.Message) {
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, ErrMisconfigured
	}
	return at, nil
}

// attachmentName is the last path element, which is what the platform's own
// clients show.
func attachmentName(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	name := parsed.Path[strings.LastIndex(parsed.Path, "/")+1:]
	if decoded, err := url.PathUnescape(name); err == nil {
		return decoded
	}
	return name
}

// checkSource binds every read to the account authenticated by these tokens.
func (a *Adapter) checkSource(ctx context.Context, source messenger.SourceID) error {
	if source.Platform != Platform || source.Account == "" {
		return messenger.ErrInvalid
	}
	account, err := a.me(ctx)
	if err != nil {
		return err
	}
	if account.ID != source.Account {
		return messenger.ErrInvalid
	}
	return nil
}

var _ messenger.Adapter = (*Adapter)(nil)
