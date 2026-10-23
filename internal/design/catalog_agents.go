// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"agents.role.system":      {"Setup and system health", "설정·시스템 관리"},
		"agents.role.lina":        {"Plans and coordination", "계획·업무 조율"},
		"agents.role.dev":         {"Development owner", "개발 담당"},
		"agents.role.knowledge":   {"People and knowledge", "인물·지식 관리"},
		"agents.role.life":        {"Hobbies and daily life", "취미·일상"},
		"agents.role.creative":    {"Creative work", "창작"},
		"agents.role.investing":   {"Investing · Advice only", "투자 · 조언만 제공"},
		"agents.role.legal":       {"Legal · Drafts only", "법률 · 초안만 작성"},
		"agents.from_description": {"From a description", "설명으로 생성"},
		"agents.advice_only":      {"Advice only", "조언만"},
		"agents.draft_only":       {"Drafts only", "초안만"},
		"agents.development":      {"Development owner", "개발 담당"},
		"agents.unavailable":      {"Selected agent unavailable", "선택한 에이전트 없음"},
		"agents.choose_required":  {"Selected agent is unavailable. Draft kept; use /agents choose <id> before sending.", "선택한 에이전트가 없습니다. 초안을 보관했습니다. /agents choose <id>로 선택 후 보내세요."},
		"agents.heading":          {"Agents", "에이전트"},
		"core.md":                 {"Core guidance", "핵심 지침"},
		"default-voice.md":        {"Default voice", "기본 말투"},
		"user-voice.md":           {"Personal voice", "사용자 말투"},
		"growth-snapshot.md":      {"Growth snapshot", "성장 기록"},
	})
}
