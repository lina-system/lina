// SPDX-License-Identifier: Apache-2.0

package design

import "strings"

type Language string

const (
	English Language = "en"
	Korean  Language = "ko"
)

func LanguageFor(locale string) Language {
	locale = strings.ToLower(locale)
	if locale == "ko" || strings.HasPrefix(locale, "ko_") || strings.HasPrefix(locale, "ko-") {
		return Korean
	}
	return English
}

// Lookup accepts only catalog ids; it never turns unknown peer text into UI.
func Lookup(language Language, id string) (string, bool) {
	entry, ok := catalog[id]
	if !ok {
		return "", false
	}
	if language == Korean {
		return entry.ko, true
	}
	return entry.en, true
}

func Text(language Language, id string) string {
	if text, ok := Lookup(language, id); ok {
		return text
	}
	text, _ := Lookup(language, "unknown")
	return text
}

type translation struct{ en, ko string }

// Keys are wire codes or local presentation ids, never English source text.
// Keep both languages together so a new entry cannot omit a translation.
var catalog = map[string]translation{
	"hello":                      {"Connect", "연결 요청"},
	"welcome":                    {"Connected", "연결됨"},
	"error":                      {"Error", "오류"},
	"conversation_start":         {"Start conversation", "대화 시작"},
	"input":                      {"Message", "메시지"},
	"interrupt":                  {"Interrupt", "중단"},
	"history_get":                {"Load history", "기록 불러오기"},
	"history":                    {"History", "기록"},
	"subscribe":                  {"Follow updates", "업데이트 받기"},
	"output_delta":               {"Writing", "작성 중"},
	"item_done":                  {"Complete", "완료"},
	"turn_state":                 {"Conversation state", "대화 상태"},
	"approval_request":           {"Approval requested", "승인 요청"},
	"approval_answer":            {"Approval decision", "승인 결정"},
	"question":                   {"Question", "질문"},
	"question_answer":            {"Answer", "답변"},
	"result":                     {"Result", "결과"},
	"result_get":                 {"Open result", "결과 열기"},
	"result_edit":                {"Edit result", "결과 수정"},
	"result_save":                {"Save result", "결과 저장"},
	"running_get":                {"Show running work", "진행 중인 작업 보기"},
	"running":                    {"Running", "진행 중"},
	"running_update":             {"Work updated", "작업 업데이트"},
	"stop":                       {"Stop", "멈추기"},
	"follow_up":                  {"Follow up", "후속 메시지"},
	"background_notice":          {"Background update", "백그라운드 알림"},
	"progress_get":               {"Show progress", "진척 보기"},
	"progress":                   {"Progress", "진척"},
	"progress_update":            {"Progress updated", "진척 업데이트"},
	"progress_digest_get":        {"Show progress summary", "진척 요약 보기"},
	"progress_digest":            {"Progress summary", "진척 요약"},
	"waiting_get":                {"Show waiting decisions", "기다리는 결정 보기"},
	"waiting":                    {"Waiting for me", "내 결정 대기"},
	"waiting_update":             {"Waiting decisions updated", "대기 중인 결정 업데이트"},
	"waiting_answer":             {"Decision", "결정"},
	"details_get":                {"Open details", "자세히 보기"},
	"details":                    {"Details", "자세히"},
	"readiness_failure":          {"Not ready", "준비되지 않음"},
	"agent_request":              {"Agent request", "에이전트 요청"},
	"agent_result":               {"Agent result", "에이전트 결과"},
	"work_request":               {"Work request", "작업 요청"},
	"work_result":                {"Work result", "작업 결과"},
	"account_request":            {"Account request", "계정 요청"},
	"account_result":             {"Account result", "계정 결과"},
	"ok":                         {"Ready", "준비됨"},
	"needs_sign_in":              {"Sign-in required", "로그인 필요"},
	"not_eligible":               {"Not eligible", "이용 자격 없음"},
	"usage_capped":               {"Usage limit reached", "사용 한도 도달"},
	"login":                      {"Sign in", "로그인"},
	"provider":                   {"Provider account", "모델 제공자 계정"},
	"memory_request":             {"Memory request", "기억 요청"},
	"memory_result":              {"Memory result", "기억 결과"},
	"memory_import_progress":     {"Memory import progress", "기억 가져오기 진척"},
	"memory_promotion_batch":     {"Review imported memories", "가져온 기억 검토"},
	"channel_request":            {"Channel request", "채널 요청"},
	"channel_result":             {"Channel result", "채널 결과"},
	"channel_state":              {"Channel state", "채널 상태"},
	"agent_talk_get":             {"Show agent exchanges", "에이전트 대화 보기"},
	"agent_talk":                 {"Agent exchanges", "에이전트 대화"},
	"agent_talk_entry":           {"Agent exchange", "에이전트 대화 항목"},
	"agent_talk_expand":          {"Expand exchange", "대화 펼치기"},
	"agent_talk_subscribe":       {"Follow agent exchanges", "에이전트 대화 업데이트 받기"},
	"connected":                  {"Connected", "연결됨"},
	"disconnected":               {"Disconnected", "연결 종료"},
	"task_status":                {"Task status", "작업 상태"},
	"run_status":                 {"Run status", "실행 상태"},
	"task_cancel":                {"Cancel task", "작업 취소"},
	"multi_task_control":         {"Manage tasks", "작업 관리"},
	"sign_in_listen":             {"Prepare sign-in", "로그인 준비"},
	"sign_in_listening":          {"Waiting for sign-in", "로그인 대기"},
	"sign_in_open":               {"Open sign-in", "로그인 열기"},
	"sign_in_redirect":           {"Sign-in response", "로그인 응답"},
	"core":                       {"Core", "Core"},
	"client":                     {"Client", "클라이언트"},
	"node":                       {"Node", "노드"},
	"none":                       {"None", "없음"},
	"refused":                    {"Refused", "거부됨"},
	"failed":                     {"Failed", "실패"},
	"applied":                    {"Applied", "반영됨"},
	"cancelled":                  {"Cancelled", "취소됨"},
	"unknown":                    {"Unknown", "알 수 없음"},
	"unsupported_combination":    {"Incompatible versions", "호환되지 않는 버전"},
	"capability_not_negotiated":  {"Unsupported capability", "지원되지 않는 기능"},
	"malformed":                  {"Invalid message", "잘못된 메시지"},
	"stale_epoch":                {"Core restarted", "Core 재시작됨"},
	"stale_revision":             {"Changed since displayed", "표시 후 변경됨"},
	"not_authorized":             {"Not authorized", "권한 없음"},
	"out_of_scope":               {"Outside the allowed scope", "허용 범위 밖"},
	"duplicate":                  {"Already received", "이미 수신됨"},
	"unavailable":                {"Unavailable", "사용할 수 없음"},
	"once":                       {"Allow once", "한 번 허용"},
	"deny":                       {"Deny", "거부"},
	"always_allow":               {"Always allow this scope", "이 범위 항상 허용"},
	"text":                       {"Text", "텍스트"},
	"number":                     {"Number", "숫자"},
	"boolean":                    {"Yes or no", "예 또는 아니요"},
	"choice":                     {"Choice", "선택"},
	"accept":                     {"Accept", "수락"},
	"decline":                    {"Decline", "거절"},
	"cancel":                     {"Cancel", "취소"},
	"summary":                    {"Summary", "요약"},
	"table":                      {"Table", "표"},
	"diagram":                    {"Diagram", "도식"},
	"chart":                      {"Chart", "차트"},
	"document_draft":             {"Document draft", "문서 초안"},
	"comparison":                 {"Comparison", "비교"},
	"plan_view":                  {"Plan", "계획"},
	"file":                       {"File", "파일"},
	"preview":                    {"Preview", "미리 보기"},
	"saved":                      {"Saved", "저장됨"},
	"connecting":                 {"Connecting", "연결 중"},
	"live":                       {"Connected", "연결됨"},
	"resyncing":                  {"Syncing", "동기화 중"},
	"snapshot":                   {"Current view", "현재 보기"},
	"resync":                     {"Refresh required", "새로 고침 필요"},
	"resync_required":            {"Updates interrupted; refresh required", "업데이트 중단; 새로 고침 필요"},
	"retrying":                   {"Reconnecting", "다시 연결 중"},
	"recovering":                 {"Recovering", "복구 중"},
	"stopped":                    {"Core is unavailable", "Core를 사용할 수 없음"},
	"quit":                       {"Disconnected", "연결 종료"},
	"epoch_changed":              {"Core restarted; refreshing", "Core가 재시작되어 새로 고치는 중"},
	"connection.connecting":      {"Opening your connection to LINA.", "LINA에 연결하고 있습니다."},
	"connection.live":            {"Your connection is ready.", "연결이 준비되었습니다."},
	"connection.resyncing":       {"Bringing your conversation up to date.", "대화를 최신 상태로 갱신하고 있습니다."},
	"connection.retrying":        {"Your conversation stays here while the connection returns.", "다시 연결하는 동안 대화는 유지됩니다."},
	"connection.refused":         {"Check that LINA and Core are compatible.", "LINA와 Core의 호환성을 확인하세요."},
	"connection.recovering":      {"You can read while Core recovers.", "Core가 복구되는 동안 기록을 읽을 수 있습니다."},
	"connection.stopped":         {"Start Core, then connect again.", "Core를 시작한 뒤 다시 연결하세요."},
	"connection.failed":          {"Check Core and your terminal, then connect again.", "Core와 터미널을 확인한 뒤 다시 연결하세요."},
	"connection.quit":            {"LINA keeps running. See you soon.", "LINA는 계속 실행됩니다. 또 만나요."},
	"connection.unknown":         {"The connection state is unknown.", "연결 상태를 알 수 없습니다."},
	"action.quit":                {"Quit", "종료"},
	"action.quit_line":           {"q + Enter  Quit", "q + Enter  종료"},
	"connection.attempt":         {"Attempt", "시도"},
	"connection.next_try":        {"Next try in", "다음 시도까지"},
	"connection.client_protocol": {"Client protocol", "클라이언트 프로토콜"},
	"connection.core_protocol":   {"Core protocol", "Core 프로토콜"},
	"shell.too_small":            {"Enlarge the terminal to at least 40 columns and 10 rows.", "터미널을 40열, 10행 이상으로 넓혀 주세요."},
	"shell.running":              {"Running work: unsupported", "진행 중인 작업: 지원되지 않음"},
	"shell.cards":                {"Waiting for me: unsupported", "내 결정 대기: 지원되지 않음"},
	"shell.composer":             {"Conversation input: unsupported", "대화 입력: 지원되지 않음"},
}
