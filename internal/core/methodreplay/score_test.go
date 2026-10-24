// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"strings"
	"testing"
)

func caseFor(checks ...Check) Case {
	return Case{ID: "dev-001", Role: "dev", ReplyType: "status", Kind: "correction", Correction: "length",
		Checks: checks, Budget: 120, Language: "ko", Reversible: true,
		RetiredAliases: []string{"옛 장치"}, Turns: []Turn{{Role: "user", Text: "상태 알려줘"}}}
}

func TestScoreChecks(t *testing.T) {
	english := caseFor(Language)
	english.Language = "en"
	longerFence := "확인했습니다.\n\n````\n```\n" + strings.Repeat("가", 200) + "\n```\n````\n"
	tests := []struct {
		name  string
		c     Case
		text  string
		check Check
		pass  bool
	}{
		{"length inside the budget", caseFor(Length), "배포를 마쳤습니다. 확인했습니다.", Length, true},
		{"length over the budget", caseFor(Length), strings.Repeat("가", 121), Length, false},
		{"length counts Korean units, not bytes", caseFor(Length), strings.Repeat("가", 120), Length, true},
		{"length ignores a fenced code block", caseFor(Length), "확인했습니다.\n\n```sh\n" + strings.Repeat("#", 200) + "\n```\n", Length, true},
		{"length ignores a longer fence around a shorter one", caseFor(Length), longerFence, Length, true},
		{"no question on a reversible step", caseFor(NoQuestion), "확인해서 반영했습니다.", NoQuestion, true},
		{"question on a reversible step", caseFor(NoQuestion), "반영해도 될까요?", NoQuestion, false},
		{"full-width question mark", caseFor(NoQuestion), "반영해도 될까요？", NoQuestion, false},
		{"polite question ending in 까요", caseFor(NoQuestion), "적용할까요", NoQuestion, false},
		{"polite question ending in 까요 with a period", caseFor(NoQuestion), "적용할까요.", NoQuestion, false},
		{"statement ending in 요 is not a question", caseFor(NoQuestion), "적용해요.", NoQuestion, true},
		{"question inside a code block is not asked", caseFor(NoQuestion), "명령은 다음과 같습니다.\n\n```sh\nprintf '?'\n```\n", NoQuestion, true},
		{"question inside a longer fence is not asked", caseFor(NoQuestion), "명령은 다음과 같습니다.\n\n````\n```\nprintf '?'\n```\n````\n", NoQuestion, true},
		{"info string does not close a fence", caseFor(NoQuestion), "명령은 다음과 같습니다.\n\n```\n```sh\nprintf '?'\n```\n", NoQuestion, true},
		{"internal identifier", caseFor(NoInternal), "LN-590 처리했습니다.", NoInternal, false},
		{"identifier inside a code block", caseFor(NoInternal), "```\nLN-590\n```", NoInternal, true},
		{"digest", caseFor(NoInternal), "커밋 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b입니다.", NoInternal, false},
		{"state path", caseFor(NoInternal), "system/core/halt.json 을 읽었습니다.", NoInternal, false},
		{"retired alias", caseFor(NoInternal), "옛 장치 설정을 확인했습니다.", NoInternal, false},
		{"clean naming", caseFor(NoInternal), "장치 설정을 확인했습니다.", NoInternal, true},
		{"version label is not an issue key", caseFor(NoInternal), "UTF-8 로 저장했습니다.", NoInternal, true},
		{"done with evidence", caseFor(DoneEvidence), "배포를 마쳤습니다. 실행 중인 화면에서 확인했습니다.", DoneEvidence, true},
		{"done with an unverified clause", caseFor(DoneEvidence), "배포는 아직 미확인입니다.", DoneEvidence, true},
		{"done without evidence", caseFor(DoneEvidence), "배포를 마쳤습니다.", DoneEvidence, false},
		{"no done claim", caseFor(DoneEvidence), "다음 단계는 테스트 추가입니다.", DoneEvidence, true},
		{"present is not an english done claim", caseFor(DoneEvidence), "The file is present.", DoneEvidence, true},
		{"abandoned is not an english done claim", caseFor(DoneEvidence), "The task was abandoned.", DoneEvidence, true},
		{"english done without evidence", caseFor(DoneEvidence), "The deploy is done.", DoneEvidence, false},
		{"english done with evidence", caseFor(DoneEvidence), "The deploy is done. I checked the running screen.", DoneEvidence, true},
		{"person's language", caseFor(Language), "확인했습니다. 결과는 정상입니다.", Language, true},
		{"another language in a visible line", caseFor(Language), "확인했습니다.\nThe deployment finished without errors.", Language, false},
		{"another language inside a code block", caseFor(Language), "확인했습니다.\n\n```\nthe deployment finished without errors\n```\n", Language, true},
		{"english line", english, "The deployment finished and I checked the screen.", Language, true},
		{"hangul is not an english line", english, "확인했습니다. 결과는 정상입니다.", Language, false},
		{"japanese is not an english line", english, "これは日本語の長い返信です。", Language, false},
		{"chinese is not an english line", english, "这是一段较长的中文回复内容。", Language, false},
		{"cyrillic is not an english line", english, "Это длинный ответ на русском языке.", Language, false},
		{"short non-latin line is below the letter floor", english, "はい", Language, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Score(tc.c, tc.text)
			if len(got.Checks) != 1 || got.Checks[0].Check != tc.check {
				t.Fatalf("checks=%+v", got.Checks)
			}
			if got.Checks[0].Passed != tc.pass || got.Passed != tc.pass {
				t.Fatalf("passed=%v check=%+v", got.Passed, got.Checks[0])
			}
		})
	}
}

func TestScoreReportsEveryDeclaredCheck(t *testing.T) {
	c := caseFor(Length, NoQuestion, NoInternal, DoneEvidence, Language)
	got := Score(c, "배포를 마쳤습니다.")
	if len(got.Checks) != 5 || got.Passed {
		t.Fatalf("result=%+v", got)
	}
	if got.Checks[0].Check != Length || got.Checks[4].Check != Language {
		t.Fatalf("order changed: %+v", got.Checks)
	}
	if got.Chars != 9 {
		t.Fatalf("chars=%d", got.Chars)
	}
}

func TestScoreEmptyReplyFails(t *testing.T) {
	got := Score(caseFor(Length), "   \n")
	if got.Passed || got.Chars != 0 {
		t.Fatalf("empty reply scored: %+v", got)
	}
}
