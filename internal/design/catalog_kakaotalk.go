// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"kakaotalk_request": {"Messenger request", "메신저 요청"},
		"kakaotalk_result":  {"Messenger result", "메신저 결과"},
		"kakaotalk":         {"KakaoTalk", "카카오톡"},
		"card":              {"Connect card", "연결 안내"},
		"secret":            {"Secret", "비밀값"},
		"url":               {"URL", "주소"},
		"code":              {"Code", "코드"},
		"signed_in":         {"Signed in", "로그인됨"},
		"kakaotalk.card": {"KakaoTalk does not allow reading a personal account with any program. LINA reads your chats through the messenger add-on as a tablet sub-device, read-only. Kakao may limit or permanently restrict your account for this, even though LINA never sends, never marks a message read and never takes another device's place. You accept this before connecting, and LINA records the acceptance with its date.",
			"카카오톡은 어떤 프로그램으로든 개인 계정을 읽는 것을 허용하지 않아요. LINA는 메신저 추가 기능으로 태블릿 보조 기기처럼 읽기만 해요. LINA는 보내지도, 읽음 표시를 하지도, 다른 기기를 밀어내지도 않지만, 이 때문에 카카오가 계정 이용을 제한하거나 영구적으로 제한할 수 있어요. 연결하기 전에 이 내용을 받아들이며, LINA는 받아들인 날짜와 함께 기록해요."},
	})
}
