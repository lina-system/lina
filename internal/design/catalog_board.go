// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"board.heading":   {"Board", "보드"},
		"board.done":      {"Done", "완료"},
		"board.keys":      {"↑/↓ select · Enter expand · r refresh · Esc close", "↑/↓ 선택 · Enter 펼치기 · r 새로고침 · Esc 돌아가기"},
		"board.empty":     {"None", "없음"},
		"board.open":      {"o open", "o 열기"},
		"board.dismiss":   {"d dismiss", "d 닫기"},
		"board.stop":      {"s stop", "s 중단"},
		"board.note":      {"n add note", "n 메모 추가"},
		"board.decisions": {"F6 decisions", "F6 응답"},
		"work.title":      {"Work", "작업"},
		"work.view":       {"View", "보기"},
		"work.filter":     {"Filter", "필터"},
		"work.open":       {"Open", "열기"},
		"work.expand":     {"Expand", "펼치기"},
		"work.stages":     {"Stages", "단계"},
		"work.details":    {"Details", "자세히"},

		"work.filter.needs_input": {"Needs you", "확인 필요"},
		"work.filter.in_progress": {"Running", "진행 중"},
		"work.filter.waiting":     {"Waiting", "대기"},
		"work.filter.done":        {"Done", "끝"},

		"work.done":             {"Done %d", "끝 %d"},
		"work.more":             {"%s %d more", "%s %d개 더"},
		"work.needs":            {"needs you %d", "확인 필요 %d"},
		"work.count":            {"%d running, %d waiting for you", "%d개 진행 중, %d개 확인 필요"},
		"work.working":          {"In progress", "진행 중"},
		"work.responding":       {"Preparing reply", "답변 작성"},
		"work.executing":        {"Running command", "명령 실행"},
		"work.editing":          {"Editing files", "파일 수정"},
		"work.tool":             {"Using tool", "도구 실행"},
		"progress.digest_count": {"%s items · %s more", "%s개 · %s개 더"},
		"progress":              {"Progress", "진척"},
		"progress.unknown":      {"unknown", "알 수 없음"},
		"progress.needs_you":    {"Needs you", "확인 필요"},
		"progress.waiting_word": {"Waiting", "대기"},
		"progress.untitled":     {"Untitled", "제목 없음"},
		"progress.design":       {"design", "설계"},
		"progress.building":     {"building", "만드는 중"},
		"progress.checking":     {"checking", "확인"},
		"progress.done":         {"done", "끝"},
		"stage.more":            {"%s and %d more", "%s 외 %d"},
		"progress.empty":        {"No work in progress", "진행 중인 일이 없어요"},
		"progress.more":         {"+%s more", "+%s개 더"},
		"progress.waiting":      {"%s waiting for you", "%s개 답변 기다리는 중"},
		"progress.line":         {"%s%s / %s / %s / latest: %s / next: %s%s", "%s%s / %s / %s / 최근: %s / 다음: %s%s"},
	})
}
