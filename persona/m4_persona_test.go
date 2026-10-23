// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

// These are contract concepts, not snapshots of the prose. Keep the checks
// small enough to allow rewriting sentences, but require each judgment's
// concepts in the same paragraph. Model behavior is evaluated by M4-4 replay.
func TestM4StarterJudgmentAndVoice(t *testing.T) {
	type judgment struct {
		name     string
		concepts []string
	}
	cases := map[string]struct {
		name, character string
		voice           []string
		judgments       []judgment
	}{
		"system": {"Selene", "귀찮", []string{"반말", "~야/~어/~지"}, []judgment{
			{"personal tools boundary", []string{"사용자", "도구", "자격 증명", "삭제", "비용", "공개", "지나친"}},
			{"own executable steps", []string{"직접", "실행", "사용자에게", "넘기지"}},
			{"standing rules", []string{"모든 에이전트", "규칙", "한 에이전트", "묻"}},
		}},
		"lina": {"Lina", "무심", []string{"주인님", "합니다체", "해요체"}, []judgment{
			{"read sources before open items", []string{"미완료", "묻기 전에", "원출처"}},
			{"review a completed legal draft", []string{"초안", "마친 뒤", "Kanna", "검토"}},
		}},
		"dev": {"Sion", "차분", []string{"마스터", "한다체", "직접 확인", "추정", "미확인"}, []judgment{
			{"peer reports remain claims", []string{"worker", "에이전트", "주장", "재현"}},
			{"done means run", []string{"완료", "병합", "실행"}},
		}},
		"knowledge": {"Rumi", "사서", []string{"마스터", "현대 반말", "~구먼", "어허"}, []judgment{
			{"one concrete flag", []string{"위험", "구체", "한 번", "경고부터"}},
			{"read before asking", []string{"묻기 전에", "읽", "기록"}},
		}},
		"life": {"Arin", "생활 큐레이터", []string{"소인", "나리", "사극 존대", "~하옵니다"}, []judgment{
			{"stable voice and explicit safety", []string{"주제", "말투", "안전", "분명"}},
			{"recorded preferences before recommendations", []string{"추천 전에", "싫", "선택", "기록"}},
			{"check owned devices", []string{"사용자", "기기", "확인", "인용"}},
		}},
		"creative": {"Cleo", "오타쿠", []string{"선생", "현대 반말", "우효~", "최애컷"}, []judgment{
			{"favourite matches stated taste", []string{"최애컷", "내기 전에", "말한 취향"}},
			{"sent handoff", []string{"전달", "실제로", "보낸 뒤"}},
			{"reversible reruns", []string{"되돌릴 수 있는", "재실행", "묻지"}},
			{"narrow vague rejection", []string{"막연", "거절", "질문 하나", "좁"}},
		}},
		"investing": {"Kai", "느긋", []string{"형", "반말", "느긋"}, []judgment{
			{"finish with assumptions", []string{"가정", "마무리", "질문"}},
			{"every figure dated and sourced", []string{"모든 수치", "출처", "기준일"}},
			{"closing price on trading day", []string{"종가", "거래일", "확인"}},
		}},
		"legal": {"Kanna", "발랄", []string{"반말", "~거든", "~잖아"}, []judgment{
			{"unasked legal risk once", []string{"묻지 않은", "법적 위험", "한 번"}},
			{"round toward stated bounds", []string{"하한", "상한", "사용자", "반올림"}},
			{"own document labels", []string{"사용자", "문서", "항목명"}},
		}},
	}
	if len(cases) != len(persona.Starters()) {
		t.Fatal("every starter needs a contract case")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			want, ok := cases[starter.Key]
			if !ok || starter.Name != want.name {
				t.Fatalf("starter identity changed: %+v", starter)
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{want.name, want.character} {
				if !strings.Contains(core, marker) {
					t.Errorf("core lost identity/character marker %q", marker)
				}
			}
			for _, marker := range want.voice {
				if !strings.Contains(voice, marker) {
					t.Errorf("voice lost marker %q", marker)
				}
			}
			for _, rule := range want.judgments {
				found := false
				for _, paragraph := range strings.Split(core, "\n\n") {
					matches := true
					for _, concept := range rule.concepts {
						matches = matches && strings.Contains(paragraph, concept)
					}
					found = found || matches
				}
				if !found {
					t.Errorf("missing M4 judgment: %s", rule.name)
				}
			}
			for layer, body := range map[string]string{"core": core, "voice": voice} {
				for name, pattern := range map[string]string{
					"topic register switch":           `(건강|심각|진지|결제|예약)[^\n.!?]*(합니다체|존댓말로|격식체로)`,
					"fixed nothing-to-decide closing": `(?i)nothing to decide|결정할 (일|것|사항)[은이]? 없|정하실 (일|것)[은이]? 없`,
				} {
					if regexp.MustCompile(pattern).MatchString(body) {
						t.Errorf("%s contains %s", layer, name)
					}
				}
			}
		})
	}
}
