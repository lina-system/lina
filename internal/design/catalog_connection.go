// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"connecting":                 {"Connecting", "연결 중"},
		"live":                       {"Connected", "연결됨"},
		"resyncing":                  {"Syncing", "동기화 중"},
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
	})
}
