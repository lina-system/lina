// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"shortcuts.transcript": {"Transcript", "대화 기록"},
		"shortcuts.title":      {"Shortcuts", "단축키"},
		"shortcuts.open":       {"Shortcuts (empty draft)", "단축키 (빈 입력창)"},
		"shortcuts.escape":     {"Close overlay / stop turn", "창 닫기 / 응답 중단"},
		"shortcuts.stop":       {"Stop", "중단"},
		"shortcuts.send":       {"Send", "보내기"},
		"shortcuts.newline":    {"Newline", "줄바꿈"},
		"shortcuts.scroll":     {"Scroll", "스크롤"},
		"shortcuts.card":       {"Open selected result", "선택한 결과 열기"},
		"shortcuts.open_hint":  {"Open", "열기"},
		"shortcuts.decisions":  {"Decisions", "결정"},
		"shortcuts.clear":      {"Clear draft / quit if empty", "입력 지우기 / 비었으면 종료"},
		"shortcuts.focus":      {"Switch input / result focus", "입력창 / 결과 간 이동"},
		"shortcuts.undo":       {"Undo draft edit", "입력 편집 취소"},
		"shortcuts.close":      {"Close", "닫기"},
		"shortcuts.search":     {"Search", "검색"},
		"shortcuts.next":       {"Next match", "다음 일치"},
		"shortcuts.pager":      {"In a detail view", "자세히 보기에서"},
		"shortcuts.queue":      {"Queue a follow-up", "후속 메시지 예약"},
	})
}
