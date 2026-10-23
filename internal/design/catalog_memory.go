// SPDX-License-Identifier: Apache-2.0

package design

func init() {
	register(map[string]translation{
		"memory.heading":         {"Memory", "기억"},
		"remember":               {"Remembered", "기억됨"},
		"correct":                {"Corrected", "정정됨"},
		"forget":                 {"Forgotten", "잊음"},
		"local":                  {"Local", "로컬"},
		"private":                {"Private", "비공개"},
		"never-leaves":           {"Never leaves", "외부 전송 금지"},
		"memory_request":         {"Memory request", "기억 요청"},
		"memory_result":          {"Memory result", "기억 결과"},
		"memory_import_progress": {"Memory import progress", "기억 가져오기 진척"},
		"memory_promotion_batch": {"Review imported memories", "가져온 기억 검토"},
	})
}
