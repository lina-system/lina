// SPDX-License-Identifier: Apache-2.0
package protocol

import "strings"

// OwnerCommand translates explicit surface commands into Core requests. It
// performs no ranking, target lookup, authorization or execution.
func OwnerCommand(text, conversation string) (Kind, Payload, bool) {
	var scope *string
	if conversation != "" {
		scope = &conversation
	}
	head, tail, _ := strings.Cut(strings.TrimSpace(text), " ")
	tail = strings.TrimSpace(tail)
	switch head {
	case "/progress":
		p := ProgressDigestGet{}
		if tail != "" {
			p.Order = &tail
		}
		return KindProgressDigestGet, p, true
	case "/waiting":
		p := WaitingGet{}
		if tail != "" {
			p.Order = &tail
		}
		return KindWaitingGet, p, true
	case "/stop":
		if tail == "all" {
			return KindMultiTaskControl, MultiTaskControl{Action: "stop_all", ConversationID: scope}, true
		}
		if tail == "" {
			return KindInterrupt, Interrupt{ConversationID: scope}, true
		}
		return KindStop, Stop{RunID: &tail, ConversationID: scope}, true
	case "/note":
		id, note, _ := strings.Cut(tail, " ")
		return KindFollowUp, FollowUp{RunID: id, Text: strings.TrimSpace(note), ConversationID: scope}, true
	}
	return "", nil, false
}
