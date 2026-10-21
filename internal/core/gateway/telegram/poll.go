// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

var errAdmission = errors.New("telegram_admission_failed")

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// poll owns the loop, not the library's updater. admit must commit the inbox
// row and next cursor together before returning. Failed admission ends this
// generation; its supervisor restarts from the durable cursor, never this stack.
func (c *client) poll(ctx context.Context, offset int64, admit func(context.Context, gotgbot.Update, int64) error) error {
	if offset < 0 || admit == nil {
		return errAdmission
	}
	delay := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := c.call(ctx, "getUpdates", map[string]any{
			"offset": offset, "limit": 100, "timeout": 25,
			"allowed_updates": []string{"message", "callback_query"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var f *failure
			wait := delay
			if errors.As(err, &f) {
				if f.class == "unauthorized" || f.class == "blocked" {
					return f
				}
				if f.retryAfter > wait {
					wait = f.retryAfter
				}
			}
			if err := c.pause(ctx, wait); err != nil {
				return err
			}
			delay = min(delay*2, 30*time.Second)
			continue
		}
		var updates []gotgbot.Update
		if json.Unmarshal(data, &updates) != nil {
			return errResponse
		}
		delay = time.Second
		for _, u := range updates {
			if u.UpdateId < offset {
				continue
			}
			if u.UpdateId == math.MaxInt64 {
				return errResponse
			}
			if err := admit(ctx, u, u.UpdateId+1); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errAdmission
			}
			offset = u.UpdateId + 1
		}
	}
}
