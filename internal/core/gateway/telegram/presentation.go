// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
)

// Shape is called once before enqueueing a ledger reply. It preserves text and
// paragraph boundaries where possible. More than three parts requires Core's
// summary and result link; it never silently truncates a result or approval.
func Shape(text, summary, link string) ([]string, error) {
	if text == "" || !utf8.ValidString(text) {
		return nil, &failure{class: "too_long"}
	}
	var parts []string
	for text != "" && len(parts) < 4 {
		units, end := 0, 0
		for index, r := range text {
			units += utf16.RuneLen(r)
			if units > 4096 {
				break
			}
			end = index + utf8.RuneLen(r)
		}
		if end < len(text) {
			if paragraph := strings.LastIndex(text[:end], "\n\n"); paragraph >= 0 {
				end = paragraph + 2
			}
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	if len(parts) <= 3 && text == "" {
		return parts, nil
	}
	fallback := summary + "\n" + link
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(link) == "" || !textFits(fallback, 4096) {
		return nil, &failure{class: "too_long"}
	}
	return []string{fallback}, nil
}

// PresentApproval reads Core's canonical request rather than taking model text
// or prebuilt buttons. A TooLong result leaves the request pending; the caller
// can send its result link without buttons and present the full request in-app.
func (a *Adapter) PresentApproval(ctx context.Context, l lease.Lease, address channels.Address, id string) (result gateway.SendResult, err error) {
	chat, thread, err := a.destination(address)
	if err != nil {
		return result, err
	}
	if a.approvals == nil {
		return result, authority.ErrNoSurface
	}
	request, err := a.approvals.Approval(ctx, id)
	if err != nil {
		return result, authority.ErrStale
	}
	binding, err := a.store.Binding(ctx, address)
	if err != nil || binding.Unreachable || !binding.Scope.Tools || request.Person != binding.Person || request.Agent != binding.Agent {
		return result, channels.ErrDenied
	}
	if request.Epoch != a.epoch || request.State != authority.Pending {
		return result, authority.ErrStale
	}
	buttons, err := approvalButtons(id)
	if err != nil {
		return result, err
	}
	undoable := "No"
	if request.Undoable {
		undoable = "Yes"
	}
	text := fmt.Sprintf("%s\nWhere: %s\nCan be undone: %s\nArguments: %s", request.What, request.Where, undoable, request.Arguments)
	err = lease.Do(ctx, l, func(ctx context.Context) error {
		message, callErr := a.client.sendMessage(ctx, chat, thread, text, &buttons)
		result, callErr = sendResult(message, callErr)
		return callErr
	})
	return result, err
}
