// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// AnswerApproval sends one decision. Core alone resolves it; Receive delivers
// the correlated outcome. A transport failure never replays the answer.
func (c *Client) AnswerApproval(ctx context.Context, answer protocol.ApprovalAnswer) (string, error) {
	return c.request(ctx, protocol.KindApprovalAnswer, answer)
}
func (c *Client) AnswerQuestion(ctx context.Context, answer protocol.QuestionAnswer) (string, error) {
	return c.request(ctx, protocol.KindQuestionAnswer, answer)
}
