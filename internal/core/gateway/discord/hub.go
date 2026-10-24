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

// member is one subscriber. Its dispatches are delivered by its own worker, so
// a subscriber that cannot store a dispatch yet never holds up the socket or
// the other subscribers.
type member struct {
	intents Intents
	handle  Handler
	done    chan error

	mu          sync.Mutex
	cond        *sync.Cond
	queue       []delivery
	outstanding []*held
	closed      bool
}

// held is one dispatch awaiting acceptance from every subscriber it was
// handed to. The shared checkpoint advances only past accepted dispatches.
type held struct {
	position  cursor
	remaining int
	dropped   atomic.Bool
}

func (held *held) cursor() string {
	data, _ := json.Marshal(held.position)
	return string(data)
}

// delivery is one dispatch for one subscriber.
type delivery struct {
	held *held
	kind string
	data json.RawMessage
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

	pendingMu   sync.Mutex
	pendingCond *sync.Cond
	pending     []*held
	advancing   int

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
	m.cond = sync.NewCond(&m.mu)
	go h.work(m)
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
	m.close()
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

// push queues one dispatch for this subscriber.
func (m *member) push(item delivery) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cond == nil {
		m.cond = sync.NewCond(&m.mu)
	}
	if m.closed {
		return false
	}
	m.queue = append(m.queue, item)
	m.outstanding = append(m.outstanding, item.held)
	m.cond.Signal()
	return true
}

// accepted drops a dispatch this subscriber just accepted from the list its
// worker settles on exit, so it is never counted twice.
func (m *member) accepted(held *held) {
	m.mu.Lock()
	for i, x := range m.outstanding {
		if x == held {
			m.outstanding = append(m.outstanding[:i], m.outstanding[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *member) close() {
	m.mu.Lock()
	m.closed = true
	if m.cond != nil {
		m.cond.Broadcast()
	}
	m.mu.Unlock()
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
		empty := len(h.members) == 0
		h.mu.Unlock()
		if empty {
			return
		}
		err := h.serve(asked)
		if h.stopped() {
			return
		}
		if errors.Is(err, ErrIntentsDenied) && h.dropContentIntent() {
			// The subscribers that asked for the refused intent were told, and
			// the rest reconnect with their own intents.
			continue
		}
		// A cancelled serve is this hub interrupting its own socket for an
		// intent change or a close: reconnect with the current union rather
		// than failing every subscriber on the token.
		if err != nil && !errors.Is(err, context.Canceled) {
			h.fail(err)
			return
		}
	}
}

// dropContentIntent removes the subscribers that asked for the privileged
// message-content intent after Discord refused it. Each is told that the intent
// was denied, so its source reports text unavailable; the subscribers that did
// not ask for it keep the session.
func (h *hub) dropContentIntent() bool {
	h.mu.Lock()
	var dropped []*member
	kept := h.members[:0]
	for _, m := range h.members {
		if m.intents&IntentMessageContent != 0 {
			dropped = append(dropped, m)
			continue
		}
		kept = append(kept, m)
	}
	h.members = kept
	h.mu.Unlock()
	for _, m := range dropped {
		select {
		case m.done <- ErrIntentsDenied:
		default:
		}
	}
	return len(dropped) > 0
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

// broadcast hands one dispatch to every subscriber, then lets each accept it
// on its own. The shared checkpoint advances only after every subscriber
// accepts the dispatch, so a deletion cannot be skipped, and a subscriber that
// must wait never holds up the socket or the others.
func (h *hub) broadcast(ctx context.Context, kind string, data json.RawMessage, position cursor) {
	h.mu.Lock()
	members := append([]*member(nil), h.members...)
	h.mu.Unlock()
	// With no subscriber, one acceptance finishes the dispatch.
	held := &held{position: position, remaining: max(len(members), 1)}
	h.pendingMu.Lock()
	h.pending = append(h.pending, held)
	h.pendingMu.Unlock()
	if len(members) == 0 {
		h.accepted(ctx, held)
		return
	}
	item := delivery{held: held, kind: kind, data: data}
	for _, m := range members {
		if !m.push(item) {
			h.accepted(ctx, held)
		}
	}
}

// accepted records one subscriber's acceptance and advances the checkpoint
// past every dispatch that is now accepted by all of its subscribers.
func (h *hub) accepted(ctx context.Context, held *held) {
	h.pendingMu.Lock()
	if held.dropped.Load() {
		h.pendingMu.Unlock()
		return
	}
	held.remaining--
	var ready []cursor
	for len(h.pending) > 0 && h.pending[0].remaining == 0 && !h.pending[0].dropped.Load() {
		ready = append(ready, h.pending[0].position)
		h.pending = h.pending[1:]
	}
	h.advancing += len(ready)
	h.pendingMu.Unlock()
	defer func() {
		h.pendingMu.Lock()
		h.advancing -= len(ready)
		if h.pendingCond != nil {
			h.pendingCond.Broadcast()
		}
		h.pendingMu.Unlock()
	}()
	for _, position := range ready {
		if ctx.Err() != nil {
			return
		}
		if err := h.advance(ctx, position); err != nil {
			h.fail(err)
			h.interrupt()
			return
		}
		h.keep(position)
	}
}

// abandon drops the dispatches this socket did not finish, so the next session
// resumes from the last checkpoint every subscriber accepted.
func (h *hub) abandon() {
	h.pendingMu.Lock()
	for _, held := range h.pending {
		held.dropped.Store(true)
	}
	h.pending = nil
	if h.pendingCond != nil {
		h.pendingCond.Broadcast()
	}
	h.pendingMu.Unlock()
}

// settleGrace bounds how long a closing session waits for dispatches it already
// read. A dispatch a subscriber still cannot store after the grace replays on
// the next session from the last accepted checkpoint.
const settleGrace = 2 * time.Second

// settle waits for the dispatches this socket already read to be accepted, so
// a dispatch that arrived before the close is stored before the session ends.
func (h *hub) settle() {
	h.pendingMu.Lock()
	if h.pendingCond == nil {
		h.pendingCond = sync.NewCond(&h.pendingMu)
	}
	cond := h.pendingCond
	h.pendingMu.Unlock()
	expired := make(chan struct{})
	timer := time.AfterFunc(settleGrace, func() {
		close(expired)
		h.pendingMu.Lock()
		cond.Broadcast()
		h.pendingMu.Unlock()
	})
	defer timer.Stop()
	h.pendingMu.Lock()
	for len(h.pending) > 0 || h.advancing > 0 {
		select {
		case <-expired:
			h.pendingMu.Unlock()
			h.abandon()
			return
		default:
		}
		cond.Wait()
	}
	h.pendingMu.Unlock()
}

// advance writes the shared checkpoint.
func (h *hub) advance(ctx context.Context, position cursor) error {
	cursor, err := json.Marshal(position)
	if err != nil {
		return err
	}
	return h.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO discord_sessions(bot,cursor) VALUES(?,?) ON CONFLICT(bot) DO UPDATE SET cursor=excluded.cursor", h.bot.Key(), string(cursor)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET cursor=? WHERE bot=?", string(cursor), h.bot.Key())
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

// work delivers this subscriber's dispatches in order. A retryable refusal is
// replayed with bounded backoff; anything else stops the session as before.
func (h *hub) work(m *member) {
	for {
		m.mu.Lock()
		for len(m.queue) == 0 && !m.closed {
			m.cond.Wait()
		}
		if len(m.queue) == 0 {
			outstanding := m.outstanding
			m.outstanding = nil
			m.mu.Unlock()
			for _, held := range outstanding {
				h.accepted(context.Background(), held)
			}
			return
		}
		item := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		if err := h.hand(m, item); err != nil {
			h.fail(err)
			h.interrupt()
			return
		}
		m.accepted(item.held)
		h.accepted(context.Background(), item.held)
	}
}

// hand passes one dispatch to one subscriber, replaying it while the
// subscriber reports that it can store it later.
func (h *hub) hand(m *member, item delivery) error {
	backoff := dispatchRetryMin
	for {
		err := m.handle(context.Background(), item.kind, item.data, item.held.cursor())
		if err == nil || !channels.Retryable(err) {
			return err
		}
		if item.held.dropped.Load() {
			// The socket ended before this subscriber could store the
			// dispatch, so the next session replays it.
			return nil
		}
		select {
		case <-h.stop:
			return nil
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
	// Settle the dispatches this socket read before dropping its identity, so a
	// late checkpoint write cannot revive the refused session.
	h.settle()
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
	defer h.settle()
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
			seq.Store(*f.Seq)
			h.broadcast(ctx, f.Type, f.Data, position)
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
