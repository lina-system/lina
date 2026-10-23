// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"account.loading": {"Checking connected accounts", "연결된 계정 확인 중"},
		"account.expired": {"Sign-in expired. Start again.", "로그인 시간이 만료되었습니다. 다시 시작하세요."},
		"account.ready":   {"Account connected. Conversation is unavailable on this Core.", "계정이 연결되었습니다. 이 Core에서는 아직 대화를 사용할 수 없습니다."},
		"account.heading": {"Connected accounts", "연결된 계정"},
		"account.empty":   {"No account connected. Use /signin to continue with ChatGPT.", "연결된 계정이 없습니다. /signin으로 ChatGPT에 로그인하세요."},
		"account.summary": {"ChatGPT accounts for this person", "이 사용자의 ChatGPT 계정"},
		"account.paste":   {"Paste the redirected URL, then press Enter", "리디렉션된 URL을 붙여넣고 Enter를 누르세요"},
		"account.help":    {"[s] Sign in  [a] Add ChatGPT  [x] Sign out  Up/Down select", "[s] 로그인  [a] ChatGPT 추가  [x] 로그아웃  위/아래 선택"},
	})
}
