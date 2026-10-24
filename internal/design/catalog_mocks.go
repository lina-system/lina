// SPDX-License-Identifier: Apache-2.0
package design

func init() {
	register(map[string]translation{
		"mock_request":     {"Mock request", "화면 시안 요청"},
		"mock_response":    {"Mock result", "화면 시안 결과"},
		"mock":             {"Mock", "화면 시안"},
		"elements":         {"Screen elements", "화면 요소"},
		"controls":         {"Controls", "조작 요소"},
		"tokens":           {"Design tokens", "디자인 토큰"},
		"network":          {"Network isolation", "네트워크 격리"},
		"errors":           {"Rendering errors", "화면 오류"},
		"layout_390":       {"Phone layout", "휴대전화 화면"},
		"layout_1280":      {"Desktop layout", "데스크톱 화면"},
		"toggle":           {"Toggle visibility", "표시 전환"},
		"page":             {"Page bundle", "페이지 묶음"},
		"png":              {"PNG image", "PNG 이미지"},
		"application/zip":  {"Page bundle", "페이지 묶음"},
		"image/png":        {"PNG image", "PNG 이미지"},
		"screen":           {"Screen", "화면"},
		"mini_app":         {"Mini app", "미니 앱"},
		"asset":            {"Asset", "파일"},
		"build":            {"Build", "만들기"},
		"all_passed":       {"All checks passed", "모든 검사 통과"},
		"no_improvement":   {"No further improvement", "추가 개선 없음"},
		"redraw_limit":     {"Redraw limit reached", "수정 횟수 도달"},
		"mock_unavailable": {"The mock could not be drawn", "화면 시안을 만들지 못함"},
	})
}
