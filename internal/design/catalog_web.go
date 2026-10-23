// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"web.heading":      {"Web access · Firecrawl", "웹 접근 · Firecrawl"},
		"web.loading":      {"Checking web access settings", "웹 접근 설정 확인 중"},
		"web.unconfigured": {"Web access is not set up.", "웹 접근이 설정되지 않았습니다."},
		"web.configured":   {"Key saved. Search and page reading are available; each request still needs network permission.", "키를 저장했습니다. 검색과 페이지 읽기를 사용할 수 있으며 네트워크 권한은 별도로 확인합니다."},
		"web.help":         {"s Set / replace key · x Remove key · r Refresh", "s 키 설정 / 교체 · x 키 제거 · r 새로고침"},
		"web.key":          {"Paste your Firecrawl API key; Enter saves. The key is hidden and is sent only to Firecrawl.", "Firecrawl API 키를 붙여넣고 Enter로 저장합니다. 키는 숨겨지며 Firecrawl에만 전송됩니다."},
	})
}
