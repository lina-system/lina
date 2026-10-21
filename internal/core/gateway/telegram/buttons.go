// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"errors"
	"regexp"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/internal/core/authority"
)

var (
	buttonID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,62}$`)
	errButton = errors.New("telegram_invalid_button")
)

func approvalButtons(id string) (gotgbot.InlineKeyboardMarkup, error) {
	if !buttonID.MatchString(id) {
		return gotgbot.InlineKeyboardMarkup{}, errButton
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
		{Text: "Allow once", CallbackData: id + ":o"},
		{Text: "Deny", CallbackData: id + ":d"},
	}}}, nil
}

func parseButton(data string) (string, authority.Choice, bool) {
	if len(data) < 3 || len(data) > 64 || data[len(data)-2] != ':' {
		return "", "", false
	}
	id := data[:len(data)-2]
	if !buttonID.MatchString(id) {
		return "", "", false
	}
	switch data[len(data)-1] {
	case 'o':
		return id, authority.Once, true
	case 'd':
		return id, authority.Deny, true
	default:
		return "", "", false
	}
}
