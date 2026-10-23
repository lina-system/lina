// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"card.stopped":       {"Stopped by Core restart", "Core 재시작으로 중지됨"},
		"card.confirm":       {"Select a number, then Enter to confirm", "번호로 선택한 뒤 Enter로 확인"},
		"card.targets_cut":   {"Targets do not fit; enlarge the terminal to confirm", "대상이 잘립니다; 터미널을 넓혀야 확인할 수 있습니다"},
		"card.question_keys": {"Tab: next field  Enter: send  Ctrl+D: decline  Ctrl+X: cancel", "Tab: 다음 항목  Enter: 전송  Ctrl+D: 거절  Ctrl+X: 취소"},
		"card.escape":        {"Esc: close without deciding", "Esc: 결정하지 않고 닫기"},
		"card.open":          {"F6: waiting for me", "F6: 내 결정 대기"},
		"waiting.empty":      {"nothing to decide now", "지금 결정할 일이 없어요"},
	})
}
