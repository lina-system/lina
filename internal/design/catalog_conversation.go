// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"conversation.start.memory": {"What do you remember about me?", "나에 대해 무엇을 기억해?"},
		"conversation.start.voice":  {"Please keep your replies brief.", "앞으로 짧게 답해 줘."},
		"conversation.start.task":   {"Help me finish a task.", "이 일을 끝까지 맡아서 해 줘."},
		"activity.thinking":         {"Thinking", "생각하는 중"},
		"activity.replying":         {"Replying", "답하는 중"},
		"activity.stopping":         {"Stopping", "중지하는 중"},
		"activity.stop":             {"Esc to stop", "Esc 중지"},
		"activity.worked_for":       {"Worked for %s", "%s 동안 작업함"},
		"tool.running":              {"running", "실행 중"},
		"tool.ok":                   {"ok", "성공"},
		"tool.failed":               {"failed", "실패"},
		"tool.more":                 {"+%d lines", "+%d줄"},
		"reply.full":                {"Full reply", "전체 답변"},
		"reply.truncated":           {"Preview truncated; full reply retained in conversation history.", "미리보기가 잘렸어요. 전체 답변은 대화 기록에 보관돼요."},
		"reply.open_pager":          {"Open in pager · Ctrl+O", "페이저로 열기 · Ctrl+O"},
		"conversation.latest":       {"Return to the latest output", "최신 출력으로 돌아가기"},
		"conversation.new_messages": {"↓ new messages · End", "↓ 새 메시지 · End"},
		"conversation.scroll_hint":  {"PgUp scroll", "PgUp 스크롤"},
	})
}
