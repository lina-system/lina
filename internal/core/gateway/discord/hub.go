// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/kit/secret"
)

// Intents are the Gateway subscription bits LINA asks for.
type Intents int

const (
	IntentGuilds         Intents = 1 << 0
	IntentGuildMessages  Intents = 1 << 9
	IntentDirectMessages Intents = 1 << 12
	// IntentMessageContent is the privileged intent that makes message text
	// available. A messenger source needs it; a channel asks for it only under
	// an explicit all_messages binding.
	IntentMessageContent Intents = 1 << 15

	baseIntents = IntentGuilds | IntentGuildMessages | IntentDirectMessages
)

// ErrIntentsDenied reports that Discord refused the requested intents. It is
// permanent for this token until the developer portal grants them.
var ErrIntentsDenied = errors.New("discord_intents_denied")

// Handler consumes one Gateway dispatch. It returns an error to stop the
// session, which every subscriber then sees and retries on its own schedule.
type Handler func(ctx context.Context, kind string, data json.RawMessage, cursor string) error

// Listener subscribes to a bot token's Gateway session. Subscribers on one
// token share one socket, and each receives every event.
type Listener struct {
	Bot       channels.Bot
	Token     secret.Secret
	DB        channels.Canon
	Transport http.RoundTripper
	// Intents are this subscriber's own needs; the session asks for the union.
	Intents Intents
	Handle  Handler
}

// Listen joins the Gateway session of a bot token, opening it when none runs,
// and returns when ctx ends or the session fails.
func Listen(ctx context.Context, l Listener) error {
	if l.DB == nil || !l.Bot.Valid() || !l.Token.Ready() || l.Handle == nil {
		return channels.ErrInvalid
	}
	h, m := joinHub(l)
	defer h.detach(m)
	select {
	case err := <-m.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type member struct {
	intents Intents
	handle  Handler
	done    chan error
}

// hub owns one bot token's socket. Subscribers join and leave while it runs.
// One Gateway session per token is what Discord expects and what the design
// requires when a channel connection and a source share a bot.
type hub struct {
	bot       channels.Bot
	token     secret.Secret
	db        channels.Canon
	http      *http.Client
	transport http.RoundTripper

	mu       sync.Mutex
	members  []*member
	socket   context.CancelFunc
	position cursor

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// hubKey names one bot token inside one Core. Canon is the Core's own store,
// so two Cores in one process never share a session, and the person's vault
// holds one token per bot account, so a bot has one token.
type hubKey struct {
	db  channels.Canon
	bot string
}

var hubs = struct {
	sync.Mutex
	byKey map[hubKey]*hub
}{byKey: map[hubKey]*hub{}}

func joinHub(l Listener) (*hub, *member) {
	key := hubKey{db: l.DB, bot: l.Bot.Key()}
	hubs.Lock()
	defer hubs.Unlock()
	for {
		h := hubs.byKey[key]
		if h == nil {
			break
		}
		if !h.stopped() {
			return h, h.attach(l.Intents, l.Handle)
		}
		// Wait for the old socket to close before publishing its replacement.
		hubs.Unlock()
		<-h.done
		hubs.Lock()
		if hubs.byKey[key] == h {
			delete(hubs.byKey, key)
		}
	}

	tr := l.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	h := &hub{
		bot: l.Bot, token: l.Token, db: l.DB, transport: tr,
		http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errUnavailable }},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	hubs.byKey[key] = h
	m := h.attach(l.Intents, l.Handle)
	go h.loop()
	return h, m
}

func (h *hub) attach(intents Intents, handle Handler) *member {
	m := &member{intents: intents, handle: handle, done: make(chan error, 1)}
	h.mu.Lock()
	before := h.unionLocked()
	h.members = append(h.members, m)
	changed := before != h.unionLocked()
	h.mu.Unlock()
	if changed {
		h.interrupt()
	}
	return m
}

func (h *hub) detach(m *member) {
	// Serialize the last detach with attaching a new subscriber.
	hubs.Lock()
	h.mu.Lock()
	before := h.unionLocked()
	for i, x := range h.members {
		if x == m {
			h.members = append(h.members[:i], h.members[i+1:]...)
			break
		}
	}
	empty := len(h.members) == 0
	if empty {
		h.stopOnce.Do(func() { close(h.stop) })
	}
	if empty || before != h.unionLocked() {
		if h.socket != nil {
			h.socket()
		}
	}
	h.mu.Unlock()
	hubs.Unlock()
	if empty {
		<-h.done
		hubs.Lock()
		key := hubKey{db: h.db, bot: h.bot.Key()}
		if hubs.byKey[key] == h {
			delete(hubs.byKey, key)
		}
		hubs.Unlock()
	}
}

func (h *hub) interrupt() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.socket != nil {
		h.socket()
		h.socket = nil
	}
}

func (h *hub) unionLocked() Intents {
	intents := baseIntents
	for _, m := range h.members {
		intents |= m.intents
	}
	return intents
}

func (h *hub) stopped() bool {
	select {
	case <-h.stop:
		return true
	default:
		return false
	}
}

// loop keeps one socket open while subscribers remain.
func (h *hub) loop() {
	defer close(h.done)
	defer h.interrupt()
	for {
		if h.stopped() {
			return
		}
		h.mu.Lock()
		asked := h.unionLocked()
		h.mu.Unlock()
		err := h.serve(asked)
		if h.stopped() {
			return
		}
		if err != nil {
			h.fail(err)
			return
		}
	}
}

func (h *hub) fail(err error) {
	hubs.Lock()
	h.stopOnce.Do(func() { close(h.stop) })
	h.mu.Lock()
	members := append([]*member(nil), h.members...)
	h.mu.Unlock()
	hubs.Unlock()
	for _, m := range members {
		select {
		case m.done <- err:
		default:
		}
	}
}

// deliver passes one dispatch to every subscriber in join order, then advances
// the shared checkpoint. A refusal stops the session before the checkpoint
// moves, so the next session resumes at this dispatch and every subscriber
// sees it again. Discord cannot page a deletion a second time, so a checkpoint
// that moved past a dispatch another subscriber could not store would lose it.
func (h *hub) deliver(ctx context.Context, kind string, data json.RawMessage, cursor string) error {
	h.mu.Lock()
	members := append([]*member(nil), h.members...)
	h.mu.Unlock()
	for _, m := range members {
		if err := h.hand(ctx, m, kind, data, cursor); err != nil {
			return err
		}
	}
	return h.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO discord_sessions(bot,cursor) VALUES(?,?) ON CONFLICT(bot) DO UPDATE SET cursor=excluded.cursor", h.bot.Key(), cursor); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET cursor=? WHERE bot=?", cursor, h.bot.Key())
		return err
	})
}

// dispatchRetry bounds the wait before a subscriber's retryable refusal is
// replayed. A held dispatch is replayed on this socket, so it neither advances
// the checkpoint nor stops the session other subscribers share.
const (
	dispatchRetryMin = 100 * time.Millisecond
	dispatchRetryMax = 2 * time.Second
)

// hand passes one dispatch to one subscriber, replaying it while the
// subscriber reports that it can store it later. Anything else stops the
// session as before.
func (h *hub) hand(ctx context.Context, m *member, kind string, data json.RawMessage, cursor string) error {
	backoff := dispatchRetryMin
	for {
		err := m.handle(ctx, kind, data, cursor)
		if err == nil || !channels.Retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, dispatchRetryMax)
	}
}

// resume reads the shared durable checkpoint, including source-only sessions.
// Older installations fall back to their channel cursor until the first dispatch.
func (h *hub) resume(ctx context.Context) cursor {
	h.mu.Lock()
	position := h.position
	h.mu.Unlock()
	var saved string
	err := h.db.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT cursor FROM discord_sessions WHERE bot=?", h.bot.Key()).Scan(&saved)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.QueryRowContext(ctx, "SELECT cursor FROM channel_connections WHERE bot=?", h.bot.Key()).Scan(&saved)
		}
		return err
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return position
	}
	var durable cursor
	if saved != "" && json.Unmarshal([]byte(saved), &durable) == nil {
		return durable
	}
	return position
}

func (h *hub) keep(position cursor) {
	h.mu.Lock()
	h.position = position
	h.mu.Unlock()
}

// reset drops the session identity the platform refused. Native receipts,
// pairing and bindings stay, so a replay cannot repeat work.
func (h *hub) reset(ctx context.Context) error {
	h.keep(cursor{})
	return h.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO discord_sessions(bot,cursor) VALUES(?,'') ON CONFLICT(bot) DO UPDATE SET cursor=''", h.bot.Key()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET cursor='' WHERE bot=?", h.bot.Key())
		return err
	})
}

// serve runs one socket until it closes. A nil return means the requested
// intents changed, so the caller replaces the socket with a fresh Identify.
func (h *hub) serve(asked Intents) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.mu.Lock()
	if h.stopped() || h.unionLocked() != asked {
		h.mu.Unlock()
		return nil
	}
	h.socket = cancel
	h.mu.Unlock()
	position := h.resume(ctx)
	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	conn, _, err := websocket.Dial(dialCtx, "wss://gateway.discord.gg/?v=10&encoding=json", &websocket.DialOptions{HTTPClient: h.http})
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return errUnavailable
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	helloCtx, cancelHello := context.WithTimeout(ctx, 30*time.Second)
	var hello frame
	err = wsjson.Read(helloCtx, conn, &hello)
	cancelHello()
	if ctx.Err() != nil {
		return nil
	}
	if code := websocket.CloseStatus(err); code != -1 {
		return h.readError(ctx, err)
	}
	var beat struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if err != nil || hello.Op != 10 || json.Unmarshal(hello.Data, &beat) != nil || beat.Interval < 1 || beat.Interval > 300000 {
		return errResponse
	}
	// A subscriber may have joined while this socket dialed. Identifing now
	// would ask Discord for fewer intents than the session has to serve, and a
	// resume that changes them is refused, so dial again with the whole set.
	h.mu.Lock()
	changed := h.unionLocked() != asked || len(h.members) == 0
	h.mu.Unlock()
	if changed {
		return nil
	}
	var seq atomic.Int64
	seq.Store(position.Seq)
	var ack atomic.Bool
	ack.Store(true)
	send := func() error {
		var n any
		if seq.Load() > 0 {
			n = seq.Load()
		}
		return wsjson.Write(ctx, conn, map[string]any{"op": 1, "d": n})
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		timer := time.NewTicker(time.Duration(beat.Interval) * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if !ack.Swap(false) || send() != nil {
					cancel()
					conn.CloseNow()
					return
				}
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-joined }()
	headers := make(http.Header)
	if secret.InjectHeader(headers, "Authorization", "", h.token) != nil {
		return errUnavailable
	}
	token := headers.Get("Authorization")
	var identify any = map[string]any{"op": 2, "d": map[string]any{"token": token, "intents": int(asked), "properties": map[string]string{"os": "linux", "browser": "LINA", "device": "LINA"}}}
	// Discord rejects a Resume that changes intents, so a different set from
	// the saved session identifies afresh.
	if position.Session != "" && position.Intents == asked {
		identify = map[string]any{"op": 6, "d": map[string]any{"token": token, "session_id": position.Session, "seq": position.Seq}}
	}
	if err := wsjson.Write(ctx, conn, identify); err != nil {
		return errUnavailable
	}
	token = ""
	for {
		var f frame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return h.readError(ctx, err)
		}
		switch f.Op {
		case 11:
			ack.Store(true)
		case 1:
			if err := send(); err != nil {
				return errUnavailable
			}
		case 7:
			return errUnavailable
		case 9:
			if err := h.reset(ctx); err != nil {
				return err
			}
			return errUnavailable
		case 0:
			if f.Seq == nil || *f.Seq < 0 {
				return errResponse
			}
			if f.Type == "READY" {
				var ready struct {
					Session string `json:"session_id"`
					User    user   `json:"user"`
				}
				if json.Unmarshal(f.Data, &ready) != nil || ready.User.ID != h.bot.Account || ready.Session == "" {
					return errResponse
				}
				position.Session = ready.Session
			}
			position.Seq, position.Intents = *f.Seq, asked
			data, _ := json.Marshal(position)
			if err := h.deliver(ctx, f.Type, f.Data, string(data)); err != nil {
				return err
			}
			h.keep(position)
			seq.Store(*f.Seq)
			h.mu.Lock()
			changed := h.unionLocked() != asked
			h.mu.Unlock()
			if changed {
				return nil
			}
		}
	}
}

// readError classifies a close. Invalid sequence and expired sessions need a
// fresh Identify; other transport closes resume from the committed cursor.
func (h *hub) readError(ctx context.Context, err error) error {
	reset, cause := sessionError(err)
	if reset {
		if err := h.reset(ctx); err != nil {
			return err
		}
	}
	return cause
}
