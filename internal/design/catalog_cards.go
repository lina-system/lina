// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"surface.sending":    {"Sending…", "보내는 중…"},
		"surface.edit_keys":  {"Enter: next  Esc: cancel", "Enter: 다음  Esc: 취소"},
		"surface.keys":       {"Ctrl+E: edit fields  Esc: close", "Ctrl+E: 항목 편집  Esc: 닫기"},
		"card.stopped":       {"Stopped by Core restart", "Core 재시작으로 중지됨"},
		"card.confirm":       {"Select a number, then Enter to confirm", "번호로 선택한 뒤 Enter로 확인"},
		"card.targets_cut":   {"Read all targets: PgUp/PgDn", "PgUp/PgDn으로 대상을 모두 읽어 주세요"},
		"card.question_keys": {"Tab: next field  Enter: send  Ctrl+D: decline  Ctrl+X: cancel", "Tab: 다음 항목  Enter: 전송  Ctrl+D: 거절  Ctrl+X: 취소"},
		"card.form_invalid":  {"That value was not accepted. Correct it, or decline or cancel.", "그 값은 받아들여지지 않았습니다. 고치거나 거절 또는 취소하세요."},
		"card.escape":        {"Esc: close without deciding", "Esc: 결정하지 않고 닫기"},
		"card.open":          {"F6: waiting for me", "F6: 내 결정 대기"},
		"waiting.empty":      {"nothing to decide now", "지금 결정할 일이 없어요"},
	})
}
