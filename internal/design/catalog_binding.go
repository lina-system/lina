// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"binding.chip":          {"Bound to %s", "%s에 연결됨"},
		"binding.none":          {"Not bound to an item", "항목에 연결되지 않음"},
		"binding.new":           {"New item", "새 항목"},
		"binding.title":         {"Move this message", "이 메시지의 대상 바꾸기"},
		"binding.current":       {"Now: %s", "현재: %s"},
		"binding.move":          {"Move binding", "연결 대상 바꾸기"},
		"binding.failed":        {"Could not move this message.", "이 메시지의 대상을 바꾸지 못했어요."},
		"binding.pending":       {"Changing…", "바꾸는 중"},
		"existing":              {"Existing item", "기존 항목"},
		"new":                   {"New item", "새 항목"},
		"binding_get":           {"Binding", "연결 정보"},
		"binding":               {"Binding", "연결 정보"},
		"binding_move":          {"Move binding", "연결 대상 바꾸기"},
		"binding_move_result":   {"Binding moved", "연결 대상 바뀜"},
		"binding_stats_get":     {"Binding counts", "연결 집계"},
		"binding_stats":         {"Binding counts", "연결 집계"},
		"binding_export":        {"Export binding cases", "연결 사례 내보내기"},
		"binding_export_result": {"Binding cases exported", "연결 사례 내보냄"},
	})
}
