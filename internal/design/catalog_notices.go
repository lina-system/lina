// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"notice.run_effect_question": {"How should the remaining actions proceed? Reads and analysis can continue; further actions need a separate authorized decision, and any unknown earlier outcome must be checked first.", "남은 작업을 어떻게 진행할까요? 읽기와 분석은 계속할 수 있어요. 추가 실행에는 별도의 승인된 결정이 필요하며, 이전 실행의 결과가 불확실하다면 먼저 확인해야 해요."},
		"notice.run_input_needed":    {"Your input is needed before this background work can continue.", "이 백그라운드 작업을 계속하려면 응답이 필요해요."},
		"notice.none":                {"none", "없음"},
		"notice.run_report":          {"Agent report: %s. Verified: %s. Unverified: %s. Open the result card (Ctrl+O) or Running (Ctrl+R).", "작업 보고: %s. 검증됨: %s. 미검증: %s. 결과 카드(Ctrl+O)나 진행 중인 작업(Ctrl+R)을 열어 보세요."},
		"notice.run_complete":        {"complete", "완료"},
		"notice.run_partial":         {"partial", "일부 완료"},
		"notice.run_failed":          {"failed", "실패"},
		"notice.run_cancelled":       {"cancelled", "취소"},
		"notice.run_needs_input":     {"needs_input", "응답 필요"},
		"notice.question_default":    {"Using the stated default: ", "말씀드린 기본값으로 진행해요: "},
		"notice.question_answer":     {"Recorded your response: ", "응답을 기록했어요: "},
		"notice.persona_changed":     {"Your speaking-style change is saved for the next reply. Say undo to restore it.", "말투 변경을 저장했어요. 다음 답변부터 적용돼요. 되돌리려면 취소해 달라고 말해 주세요."},
		"notice.persona_undone":      {"Your previous speaking style is restored for the next reply.", "이전 말투로 되돌렸어요. 다음 답변부터 적용돼요."},
		"notice.emotion_off":         {"Emotional expression is off.", "감정 표현을 껐어요."},
		"notice.emotion_on":          {"Emotional expression is on.", "감정 표현을 켰어요."},
		"notice.skill_changed":       {"Learned a procedure in %s.", "%s의 절차를 익혔어요."},
		"notice.skill_undone":        {"Undid the change to %s.", "%s의 변경을 되돌렸어요."},
		"notice.handoff":             {"The gathered work is saved and continues in the background.", "모은 내용을 저장했고 작업은 백그라운드에서 계속돼요."},
		"reply.request_limit":        {"Request limit", "요청 한도"},
		"notice.request_too_large":   {"This request does not fit the model’s context window.", "요청이 모델의 컨텍스트 한도를 넘었어요."},
		"notice.sign_in":             {"Sign in to your model account to continue.", "모델 계정에 로그인해 주세요."},
		"notice.failed":              {"The reply could not be completed. Send a new message to continue.", "답변을 마치지 못했어요. 계속하려면 새 메시지를 보내 주세요."},
		"notice.person":              {"You stopped this reply.", "요청하신 대로 답변을 멈췄어요."},
		"notice.watchdog":            {"This reply stopped because it was no longer making progress.", "답변이 더 진행되지 않아 멈췄어요."},
		"notice.epoch_lost":          {"This reply was interrupted by a restart. Please send a new message to continue.", "재시작으로 답변이 중단됐어요. 계속하려면 새 메시지를 보내 주세요."},
		"notice.grant_revoked":       {"This reply stopped because its permission was withdrawn.", "권한이 철회되어 답변을 멈췄어요."},
		"notice.budget":              {"This input is waiting because its automatic work allowance was reached.", "자동 작업 한도에 도달해 이 입력은 대기 중이에요."},
		"notice.shutdown":            {"This reply stopped because LINA shut down. Please send a new message to continue.", "LINA가 종료되어 답변이 멈췄어요. 계속하려면 새 메시지를 보내 주세요."},
		"notice.interrupted":         {"This reply was interrupted. Please send a new message to continue.", "답변이 중단됐어요. 계속하려면 새 메시지를 보내 주세요."},
		"sandbox.unavailable":        {"File and shell tools are unavailable. Check the sandbox setup.", "파일과 명령 실행 도구를 사용할 수 없어요. 샌드박스 설정을 확인해 주세요."},
	})
}
