// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Identify verifies the credential's bot and resolves the username used for
// addressed messages. Call before Run; construction remains local and READY
// never waits for platform availability.
func (a *Adapter) Identify(ctx context.Context) error {
	data, err := a.client.call(ctx, "getMe", nil)
	if err != nil {
		return err
	}
	var user struct {
		ID       int64  `json:"id"`
		Bot      bool   `json:"is_bot"`
		Username string `json:"username"`
	}
	if json.Unmarshal(data, &user) != nil || !user.Bot || strconv.FormatInt(user.ID, 10) != a.bot.Account || user.Username == "" {
		return errCredential
	}
	a.username = user.Username
	return nil
}

// RecoveryDelay classifies failures of read-only identity/poll admission. Effect
// sends have separate durable retry semantics and must not use this helper.
func RecoveryDelay(err error) (time.Duration, bool) {
	if errors.Is(err, errAdmission) || errors.Is(err, errTransport) {
		return time.Second, true
	}
	var failure *failure
	if errors.As(err, &failure) {
		switch failure.class {
		case "retry_later":
			return max(time.Second, failure.retryAfter), true
		case "unknown":
			return time.Second, true
		}
	}
	return 0, false
}

// Close releases idle transport resources after the supervisor has joined Run.
func (a *Adapter) Close() { a.client.http.CloseIdleConnections() }
