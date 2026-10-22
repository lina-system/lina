// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/secret"
	"net/http"
	"sync/atomic"
	"time"
)

type frame struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
	Seq  *int64          `json:"s,omitempty"`
	Type string          `json:"t,omitempty"`
}
type cursor struct {
	Session string `json:"session"`
	Seq     int64  `json:"seq"`
}

func (a *Adapter) Run(ctx context.Context, in gateway.Admitter) error {
	if in == nil {
		return channels.ErrInvalid
	}
	if _, loaded := sessions.LoadOrStore(a.bot.Key(), true); loaded {
		return channels.ErrConflict
	}
	defer sessions.Delete(a.bot.Key())
	if err := a.resumeButtons(ctx); err != nil {
		return err
	}
	saved, err := a.store.Cursor(ctx, a.bot)
	if err != nil {
		return err
	}
	var position cursor
	if saved != "" && json.Unmarshal([]byte(saved), &position) != nil {
		return errResponse
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	conn, _, err := websocket.Dial(dialCtx, "wss://gateway.discord.gg/?v=10&encoding=json", &websocket.DialOptions{HTTPClient: a.http})
	cancelDial()
	if err != nil {
		return errUnavailable
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	helloCtx, cancelHello := context.WithTimeout(ctx, 30*time.Second)
	var hello frame
	err = wsjson.Read(helloCtx, conn, &hello)
	cancelHello()
	var h struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if err != nil || hello.Op != 10 || json.Unmarshal(hello.Data, &h) != nil || h.Interval < 1 || h.Interval > 300000 {
		return errResponse
	}
	var seq atomic.Int64
	seq.Store(position.Seq)
	var ack atomic.Bool
	ack.Store(true)
	beat := func() error {
		var n any
		if seq.Load() > 0 {
			n = seq.Load()
		}
		return wsjson.Write(ctx, conn, map[string]any{"op": 1, "d": n})
	}
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		timer := time.NewTicker(time.Duration(h.Interval) * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if !ack.Swap(false) || beat() != nil {
					cancel()
					conn.CloseNow()
					return
				}
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-joined }()
	headers := make(http.Header)
	if secret.InjectHeader(headers, "Authorization", "", a.token) != nil {
		return errUnavailable
	}
	token := headers.Get("Authorization")
	var identify any = map[string]any{"op": 2, "d": map[string]any{"token": token, "intents": 1 | 512 | 4096 | 32768, "properties": map[string]string{"os": "linux", "browser": "LINA", "device": "LINA"}}}
	if position.Session != "" {
		identify = map[string]any{"op": 6, "d": map[string]any{"token": token, "session_id": position.Session, "seq": position.Seq}}
	}
	if wsjson.Write(ctx, conn, identify) != nil {
		return errUnavailable
	}
	identify = nil
	token = ""
	for {
		var f frame
		if wsjson.Read(ctx, conn, &f) != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			return errUnavailable
		}
		switch f.Op {
		case 11:
			ack.Store(true)
		case 1:
			if beat() != nil {
				return errUnavailable
			}
		case 7:
			return errUnavailable
		case 9:
			// An invalid session cannot resume. Keep all native receipts, reset only
			// session identity, and let the supervisor reconnect with backoff.
			err := a.db.Write(ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET cursor='' WHERE bot=?", a.bot.Key())
				if err != nil {
					return err
				}
				return nil
			})
			if err != nil {
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
				if json.Unmarshal(f.Data, &ready) != nil || ready.User.ID != a.bot.Account || ready.Session == "" {
					return errResponse
				}
				position.Session = ready.Session
			}
			position.Seq = *f.Seq
			data, _ := json.Marshal(position)
			if err := a.dispatch(ctx, in, f.Type, f.Data, string(data)); err != nil {
				return err
			}
			seq.Store(*f.Seq)
		}
	}
}
