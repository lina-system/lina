// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"talk.heading":      {"Agent talk log", "에이전트 대화 기록"},
		"talk.empty":        {"No exchanges", "교환 없음"},
		"talk.more":         {"Enter next page", "Enter 다음 페이지"},
		"talk.help":         {"Up/Down \u00b7 Enter expand \u00b7 r Refresh \u00b7 Esc \u00b7 Ctrl+D \u00b7 PgUp/PgDown", "위/아래 · Enter 펼치기 · r 새로고침 · Esc · Ctrl+D · PgUp/PgDown"},
		"talk.needs_person": {"Needs your decision", "내 결정 필요"},
	})
}
