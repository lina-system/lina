// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
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
	return c.sendText(ctx, chat, thread, text, buttons, false)
}

func (c *client) sendText(ctx context.Context, chat, thread int64, text string, buttons *gotgbot.InlineKeyboardMarkup, formatted bool) (int64, error) {
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
	data, err := c.textCall(ctx, "sendMessage", params, formatted)
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
	return c.editText(ctx, chat, message, text, buttons, false)
}

func (c *client) editText(ctx context.Context, chat, message int64, text string, buttons *gotgbot.InlineKeyboardMarkup, formatted bool) error {
	if !textFits(text, 4096) {
		return &failure{class: "too_long"}
	}
	if buttons == nil {
		buttons = &gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{}}
	}
	_, err := c.textCall(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": message, "text": text, "reply_markup": buttons}, formatted)
	var f *failure
	if errors.As(err, &f) && f.class == "not_modified" {
		return nil
	}
	return err
}

func (c *client) textCall(ctx context.Context, method string, params map[string]any, formatted bool) (json.RawMessage, error) {
	plain := params["text"].(string)
	if formatted {
		text, entities := formatMessage(plain)
		if len(entities) > 0 {
			params["text"], params["entities"] = text, entities
		}
	}
	data, err := c.call(ctx, method, params)
	var f *failure
	if errors.As(err, &f) && f.class == "format_rejected" && params["entities"] != nil {
		// A definite parse refusal has no delivery effect. Retain the same
		// destination/message/keyboard, and fall back once. Never retry an
		// ambiguous response, timeout, or any part that already has a receipt.
		params["text"] = plain
		delete(params, "entities")
		return c.call(ctx, method, params)
	}
	return data, err
}

func (c *client) answerCallback(ctx context.Context, id, text string) error {
	if !textFits(text, 200) {
		return &failure{class: "too_long"}
	}
	_, err := c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text})
	return err
}
