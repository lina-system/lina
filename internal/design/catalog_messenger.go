// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"messenger_request": {"Messenger request", "메신저 요청"},
		"messenger_result":  {"Messenger result", "메신저 결과"},
		"webex":             {"Webex", "Webex"},
		"configured":        {"Configured", "설정됨"},
		"failing":           {"Failing", "실패"},
		"unreachable":       {"Unreachable", "연결할 수 없음"},
		"import_only":       {"Import only", "가져오기만"},
	})
}
