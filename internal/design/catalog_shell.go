// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"usage.unknown":             {"Usage unknown", "사용량 알 수 없음"},
		"command.unknown":           {"Unknown command. Use /help.", "알 수 없는 명령입니다. /help를 확인하세요."},
		"note.help":                 {"Use /note <run> <text>, or choose a run in /board.", "/note <run> <text>를 입력하거나 /board에서 작업을 선택하세요."},
		"shell.running":             {"Running work: unsupported", "진행 중인 작업: 지원되지 않음"},
		"shell.cards":               {"Waiting for me: unsupported", "내 결정 대기: 지원되지 않음"},
		"shell.quit_hint":           {"Ctrl+C again to quit", "Ctrl+C를 다시 누르면 종료"},
		"controls.help":             {"Commands", "명령"},
		"shell.too_small":           {"Enlarge the terminal to at least 40 columns and 10 rows.", "터미널을 40열, 10행 이상으로 넓혀 주세요."},
		"shell.lists_unavailable":   {"This Core has no work or decision lists.", "이 Core는 작업·결정 목록을 제공하지 않습니다."},
		"shell.running_unavailable": {"This Core cannot show running work.", "이 Core는 진행 중인 작업 목록을 제공하지 않습니다."},
		"shell.waiting_unavailable": {"This Core cannot show waiting decisions.", "이 Core는 대기 중인 결정 목록을 제공하지 않습니다."},
		"shell.composer":            {"Conversation input: unsupported", "대화 입력: 지원되지 않음"},
		"composer.placeholder":      {"Talk to %s · Enter to send · Alt+Enter for a new line", "%s에게 말하기 · Enter 보내기 · Alt+Enter 줄바꿈"},
		"composer.shift_enter":      {"Talk to %s · Enter to send · Shift+Enter for a new line", "%s에게 말하기 · Enter 보내기 · Shift+Enter 줄바꿈"},
	})
}
