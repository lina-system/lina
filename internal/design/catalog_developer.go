// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"developer.state.active":           {"Working", "진행 중"},
		"developer.state.idle":             {"Idle", "대기 중"},
		"developer.state.waiting":          {"Waiting for input", "응답 대기"},
		"developer.state.dead":             {"Session ended", "세션 종료"},
		"developer.state.working":          {"Working", "진행 중"},
		"developer.state.input_needed":     {"Input needed", "응답 필요"},
		"developer.state.ready_for_review": {"Ready for review", "검토 준비됨"},
		"developer.state.done":             {"Done", "완료"},
		"developer.state.failed":           {"Failed", "실패"},
		"developer.state.stopped":          {"Stopped", "중지됨"},
		"developer.state.unknown":          {"Unknown", "알 수 없음"},
		"developer.heading":                {"Worker lanes", "작업자 lane"},
		"developer.keys":                   {"↑/↓ Select · Enter Fold/unfold · r Refresh · n Next page · Esc Back", "↑/↓ 선택 · Enter 접기/펼치기 · r 새로고침 · n 다음 쪽 · Esc 돌아가기"},
		"developer.empty":                  {"No recorded worker sessions", "기록된 작업자 세션이 없어요"},
		"developer.more":                   {"n Next worker page", "n 다음 작업자 쪽"},
	})
}
