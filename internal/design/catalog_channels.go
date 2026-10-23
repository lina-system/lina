// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"channel.unbind":     {"Up/Down binding · Enter unbind", "위/아래 바인딩 · Enter 해제"},
		"channel.heading":    {"Channels", "채널"},
		"channel.empty":      {"No connections", "연결 없음"},
		"channel.help":       {"c Connect \u00b7 d Discord \u00b7 p Pair \u00b7 b Bind / scope \u00b7 u Unbind \u00b7 r Refresh \u00b7 x Disconnect", "c 연결 · d Discord · p 페어링 · b 바인딩 / 범위 · u 바인딩 해제 · r 새로고침 · x 연결 해제"},
		"channel.token":      {"Paste the bot token; Enter connects. It is never shown.", "봇 토큰을 붙여넣고 Enter로 연결합니다. 토큰은 표시되지 않습니다."},
		"channel.chat":       {"Chat: Enter uses the paired private chat.", "채팅: 비워 두고 Enter를 누르면 페어링한 개인 채팅을 사용합니다."},
		"channel.topic":      {"Topic: leave empty for the whole chat.", "토픽: 채팅 전체에는 빈칸을 사용합니다."},
		"channel.scope":      {"Up/Down agent \u00b7 r reads \u00b7 w writes \u00b7 t tools \u00b7 m memory \u00b7 Enter bind. Reading every message uses platform traffic.", "위/아래 에이전트 · r 읽기 · w 쓰기 · t 도구 · m 기억 · Enter 바인딩. 모든 메시지 읽기는 플랫폼 트래픽을 사용합니다."},
		"channel.pair_help":  {"Send this single-use code to the bot in your private chat, then refresh. Expires at:", "개인 채팅에서 봇에게 이 일회용 코드를 보낸 뒤 새로고침하세요. 만료:"},
		"channel.tools_on":   {"Tools allowed", "도구 허용"},
		"channel.tools_off":  {"Tools off", "도구 금지"},
		"channel.memory_on":  {"Personal memory allowed", "개인 기억 허용"},
		"channel.memory_off": {"Personal memory off", "개인 기억 금지"},
	})
}
