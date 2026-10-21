// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func textFits(text string, limit int) bool {
	if !utf8.ValidString(text) || text == "" {
		return false
	}
	units := 0
	for _, r := range text {
		units += utf16.RuneLen(r)
		if units > limit {
			return false
		}
	}
	return true
}

func (c *client) sendMessage(ctx context.Context, chat, thread int64, text string, buttons *gotgbot.InlineKeyboardMarkup) (int64, error) {
	if !textFits(text, 4096) {
		return 0, &failure{class: "too_long"}
	}
	params := map[string]any{"chat_id": chat, "text": text}
	if thread != 0 {
		params["message_thread_id"] = thread
	}
	if buttons != nil {
		params["reply_markup"] = buttons
	}
	data, err := c.call(ctx, "sendMessage", params)
	if err != nil {
		return 0, err
	}
	var message gotgbot.Message
	if json.Unmarshal(data, &message) != nil || message.MessageId <= 0 {
		return 0, &failure{class: "unknown"}
	}
	return message.MessageId, nil
}

func (c *client) editMessage(ctx context.Context, chat, message int64, text string, buttons *gotgbot.InlineKeyboardMarkup) error {
	if !textFits(text, 4096) {
		return &failure{class: "too_long"}
	}
	if buttons == nil {
		buttons = &gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}}
	}
	_, err := c.call(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": message, "text": text, "reply_markup": buttons})
	return err
}

func (c *client) answerCallback(ctx context.Context, id, text string) error {
	if !textFits(text, 200) {
		return &failure{class: "too_long"}
	}
	_, err := c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text})
	return err
}
