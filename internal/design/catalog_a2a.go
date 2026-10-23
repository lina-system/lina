// SPDX-License-Identifier: Apache-2.0
package design

func init() {
	register(map[string]translation{
		"a2a_request":       {"Remote agent request", "원격 에이전트 요청"},
		"a2a_result":        {"Remote agent result", "원격 에이전트 결과"},
		"research":          {"Research", "조사"},
		"calculation":       {"Calculation", "계산"},
		"private_draft":     {"Private draft", "비공개 초안"},
		"external_effect":   {"External action", "외부 작업"},
		"investment_order":  {"Investment order", "투자 주문"},
		"binding_execution": {"Binding action", "구속력 있는 실행"},
		"unclassified":      {"Needs classification", "분류 필요"},
		"configure":         {"Configure", "설정"},
		"revoke":            {"Revoke access", "접근 철회"},
		"credential":        {"Credential", "인증 정보"},
		"calls":             {"Remote calls", "원격 호출"},
		"answer":            {"Answer", "답변"},
		"verify_result":     {"Verify result", "결과 검증"},
	})
}
