// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"records.zone_unavailable":         {"Set TZ to your timezone, such as Asia/Seoul, to use dated views.", "날짜 화면을 사용하려면 TZ를 Asia/Seoul 같은 시간대로 설정하세요."},
		"records.saved":                    {"Saved", "저장됨"},
		"records.record_operation_unknown": {"Save outcome is unknown. Check the record list before trying again.", "저장 결과를 확인할 수 없습니다. 다시 시도하기 전에 기록 목록을 확인하세요."},
		"records.heading":                  {"Records", "기록"},
		"records.add":                      {"Quick add", "빠른 추가"},
		"records.add_help":                 {"/add <text>", "/add <내용>"},
		"records.help":                     {"/records memos|today|upcoming|anytime|later|board|list|week|all [search]", "/records memos|today|upcoming|anytime|later|board|list|week|all [검색어]"},
		"records.keys":                     {"↑/↓ Select · Enter Open · r Refresh · Ctrl+N Next page · Esc Back", "↑/↓ 선택 · Enter 열기 · r 새로고침 · Ctrl+N 다음 쪽 · Esc 돌아가기"},
		"records.loading":                  {"Loading records…", "기록을 불러오는 중…"},
		"records.empty":                    {"No records here.", "기록이 없습니다."},
		"records.preview":                  {"Review before saving", "저장 전 확인"},
		"records.confirm":                  {"Enter Save · Esc Cancel", "Enter 저장 · Esc 취소"},
		"records.more":                     {"Ctrl+N Next page", "Ctrl+N 다음 쪽"},
		"records.overdue":                  {"Overdue", "밀린 것"},
		"records.blocked":                  {"Waiting on another task", "다른 할 일을 기다리는 중"},
		"records.none":                     {"None", "없음"},
		"records.important":                {"Important", "중요"},
		"records.chip.when":                {"Date", "날짜"},
		"records.chip.deadline":            {"Deadline", "마감"},
		"records.chip.important":           {"Priority", "우선순위"},
		"records.chip.tag":                 {"Tag", "태그"},
		"records.chip.repeat":              {"Repeat", "반복"},
		"records.chip.project":             {"Project", "일"},
		"records.state.backlog":            {"Later", "나중에"},
		"records.state.open":               {"To do", "할 일"},
		"records.state.started":            {"Started", "하는 중"},
		"records.state.claimed":            {"Not verified", "미검증"},
		"records.state.completed":          {"Done", "끝"},
		"records.state.canceled":           {"Canceled", "안 하기로 함"},
		"records.records_changed":          {"Records changed outside LINA. Review the changes before saving.", "LINA 밖에서 기록이 바뀌었습니다. 저장 전에 변경 내용을 확인하세요."},
		"records.stale_revision":           {"The record changed. Open it again.", "기록이 바뀌었습니다. 다시 열어 주세요."},
		"records.record_not_found":         {"Record not found. Refresh the list.", "기록을 찾을 수 없습니다. 목록을 새로고침하세요."},
		"records.invalid_record":           {"Check the record fields and try again.", "기록 내용을 확인한 뒤 다시 시도하세요."},
		"records.ambiguous_date":           {"Use an exact date: YYYY-MM-DD.", "날짜를 YYYY-MM-DD 형식으로 입력하세요."},
		"records.dependency_cycle":         {"These tasks would depend on each other.", "할 일이 서로를 기다리게 됩니다."},
		"records.preview_expired":          {"This preview expired. Open quick add again.", "미리보기가 만료되었습니다. 빠른 추가를 다시 여세요."},
	})
}
