// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"running.state.in_progress":      {"In progress", "진행 중"},
		"running.state.waiting":          {"Waiting", "대기 중"},
		"running.state.needs_input":      {"Input needed", "응답 필요"},
		"running.state.ready_for_review": {"Ready for review", "검토 준비됨"},
		"running.state.failed":           {"Failed", "실패"},
		"running.state.stopped":          {"Stopped", "중지됨"},
		"running.state.done":             {"Done", "완료"},
		"running.state.unknown":          {"Unknown", "알 수 없음"},
		"running.reason.dependency":      {"Waiting for a dependency", "선행 작업 대기"},
		"running.reason.area":            {"Waiting for the work area", "작업 공간 대기"},
		"running.reason.quota":           {"Waiting for quota", "사용 한도 대기"},
		"running.reason.host_capacity":   {"Waiting for host capacity", "기기 여유 자원 대기"},
		"running.reason.ci_pending":      {"Waiting for CI", "CI 결과 대기"},
		"running.reason.run_limit":       {"Waiting for a run slot", "실행 순서 대기"},
		"running.reason.person":          {"Waiting for you", "내 응답 대기"},
		"running.reason.budget":          {"Waiting for work allowance", "작업 한도 대기"},
		"running.keys":                   {"Enter open · ↑/↓ select · s stop · d dismiss · Esc close", "Enter 열기 · ↑/↓ 선택 · s 중단 · d 닫기 · Esc 돌아가기"},
		"running.next":                   {"Ctrl+N: next page", "Ctrl+N: 다음 페이지"},
		"running":                        {"Running", "진행 중"},
		"control.stopped":                {"Stop requested.", "중단을 요청했습니다."},
		"control.note":                   {"Note queued.", "추가 요청을 전달했습니다."},
		"control.stopped_all":            {"Stop requested for %s runs.", "실행 %s개의 중단을 요청했습니다."},
	})
}
