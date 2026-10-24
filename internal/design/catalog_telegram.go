// SPDX-License-Identifier: Apache-2.0
package design

func init() {
	register(map[string]translation{
		"telegram_request":  {"Telegram source request", "Telegram 소스 요청"},
		"telegram_result":   {"Telegram source result", "Telegram 소스 결과"},
		"telegram.heading":  {"Telegram account", "Telegram 계정"},
		"telegram.setup":    {"Create your own application at https://my.telegram.org. Enter its api_id and api_hash, then your phone number.", "https://my.telegram.org에서 내 앱을 만드세요. api_id와 api_hash, 전화번호를 입력하세요."},
		"telegram.api_id":   {"Application api_id · Enter continues", "앱 api_id · Enter 다음"},
		"telegram.api_hash": {"Application api_hash · Enter continues", "앱 api_hash · Enter 다음"},
		"telegram.phone":    {"Phone number with country code · Enter sends a code", "국가 코드가 포함된 전화번호 · Enter 인증 코드 요청"},
		"telegram.code":     {"Telegram sign-in code · Enter continues", "Telegram 인증 코드 · Enter 다음"},
		"telegram.password": {"Two-step verification password · Enter signs in", "2단계 인증 비밀번호 · Enter 로그인"},
		"telegram.help":     {"c Connect · r Refresh · Up/Down select · x Disconnect · Esc close", "c 연결 · r 새로고침 · 위/아래 선택 · x 연결 해제 · Esc 닫기"},
	})
}
