// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"surface":           {"Card", "카드"},
		"surface_action":    {"Card response", "카드 응답"},
		"1.0":               {"1.0", "1.0"},
		"1.1":               {"1.1", "1.1"},
		"start":             {"Start", "시작"},
		"center":            {"Center", "가운데"},
		"end":               {"End", "끝"},
		"stretch":           {"Stretch", "늘이기"},
		"vertical":          {"Vertical", "세로"},
		"horizontal":        {"Horizontal", "가로"},
		"default":           {"Default", "기본"},
		"primary":           {"Primary", "주요"},
		"borderless":        {"Borderless", "테두리 없음"},
		"spaceBetween":      {"Space between", "항목 사이 간격"},
		"spaceAround":       {"Space around", "항목 주위 간격"},
		"spaceEvenly":       {"Space evenly", "같은 간격"},
		"mutuallyExclusive": {"Choose one", "하나 선택"},
		"multipleSelection": {"Choose several", "여러 개 선택"},
		"checkbox":          {"Checkbox", "선택 상자"},
		"chips":             {"Choices", "선택 항목"},
		"date":              {"Date", "날짜"},
		"shortText":         {"Short text", "짧은 글"},
		"longText":          {"Long text", "긴 글"},
		"h1":                {"Heading 1", "제목 1"},
		"h2":                {"Heading 2", "제목 2"},
		"h3":                {"Heading 3", "제목 3"},
		"h4":                {"Heading 4", "제목 4"},
		"h5":                {"Heading 5", "제목 5"},
		"caption":           {"Caption", "설명"},
		"body":              {"Body", "본문"},
	})
}
