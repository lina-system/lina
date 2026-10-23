// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

// The M4 persona revision (docs/design/preset-work-methods.md, Starter records
// and persona changes) adds judgment rules to the core layer, removes a
// topic-dependent register switch from default voice and keeps each persona's
// name, character and voice. A requirement below is the contract's concept set,
// not a sentence: every group lists wordings that any faithful revision keeps,
// and the requirement is met when one paragraph carries one member of each
// group. Rewording or reordering passes; dropping a rule or separating its
// concepts fails. The matcher's own tests prove both directions with rewritten
// text. Model behaviour is measured by M4-4 (#590); this test guards the content
// revision that the starter records were designed against.
type personaRequirement struct {
	change string
	groups [][]string
}

type personaContract struct {
	name      string
	character []string
	voice     []string
	core      []personaRequirement
	spoken    []personaRequirement
}

func m4Contracts() map[string]personaContract {
	return map[string]personaContract{
		"system": {
			name:      "Selene",
			character: []string{"귀찮"},
			voice:     []string{"반말", "~야/~어/~지", "다른 에이전트에게는"},
			core: []personaRequirement{
				{"personal tools are not over-guarded", [][]string{
					{"사용자 자신의 도구", "사용자의 도구", "사용자 도구"},
					{"자격 증명", "자격증명"},
					{"삭제"},
					{"비용"},
					{"공개"},
					{"지나친", "과도한", "넘어선", "지나치게"},
					{"제한", "제약", "통제"},
				}},
				{"an executable step is the agent's", [][]string{
					{"직접 실행", "직접 할", "직접 하는"},
					{"사용자에게", "사용자한테"},
					{"넘기지", "미루지", "떠넘기지"},
				}},
				{"an all-agent rule is not excepted unasked", [][]string{
					{"모든 에이전트", "전체 에이전트", "모든 에이전트에"},
					{"규칙"},
					{"한 에이전트", "단일 에이전트", "특정 에이전트"},
					{"묻", "물어", "확인"},
				}},
			},
		},
		"lina": {
			name:      "Lina",
			character: []string{"무심"},
			voice:     []string{"주인님", "합니다체", "해요체", "다른 에이전트에게는"},
			core: []personaRequirement{
				{"read sources before open items or asking", [][]string{
					{"미완료", "남은 일", "열린 일", "할 일"},
					{"나열", "물어", "묻"},
					{"묻기 전에", "묻기 앞서", "물어보기 전에", "물어보기 앞서", "질문하기 전에"},
					{"원출처", "원문", "출처", "원래 자료"},
				}},
				{"finish a legal draft before review", [][]string{
					{"초안"},
					{"마친 뒤", "마치고", "끝낸 뒤", "완성한 뒤", "마친 다음", "끝낸 다음"},
					{"Kanna"},
					{"검토", "리뷰"},
				}},
			},
		},
		"dev": {
			name:      "Sion",
			character: []string{"차분"},
			voice:     []string{"마스터", "한다체", "출처"},
			core: []personaRequirement{
				{"a peer report is a claim until reproduced", [][]string{
					{"worker", "워커"},
					{"다른 에이전트", "동료", "에이전트"},
					{"주장"},
					{"재현", "확인"},
					{"전까지", "때까지", "전에는"},
				}},
				{"done is what ran, not what merged", [][]string{
					{"완료"},
					{"병합", "merge", "머지"},
					{"실행", "돌려", "run"},
					{"아니라", "아닌"},
				}},
			},
		},
		"knowledge": {
			name:      "Rumi",
			character: []string{"사서"},
			voice:     []string{"마스터", "~구먼", "어허", "다른 에이전트에게는"},
			core: []personaRequirement{
				{"one concrete flag instead of warning-led framing", [][]string{
					{"경고부터", "경고를 앞세우지", "경고로 시작", "경고를 먼저"},
					{"위험", "우려"},
					{"하나", "한 가지"},
					{"구체", "분명", "특정"},
					{"한 번", "한번"},
				}},
				{"read a source or record before asking", [][]string{
					{"묻기 전에", "묻기 앞서", "질문하기 전에", "물어보기 전에"},
					{"읽", "봐", "확인"},
					{"출처", "기록", "자료"},
				}},
			},
		},
		"life": {
			name:      "Arin",
			character: []string{"생활 큐레이터"},
			voice:     []string{"소인", "나리", "사극 존대", "~하옵니다"},
			core: []personaRequirement{
				{"same voice on every topic with explicit safety", [][]string{
					{"주제", "화제"},
					{"말투", "말씨", "목소리"},
					{"그대로", "같은", "유지", "바꾸지"},
					{"안전"},
					{"분명", "명확", "또렷"},
				}},
				{"recorded dislikes and choices come before a recommendation", [][]string{
					{"추천 전에", "권하기 전에", "권하기 앞서"},
					{"싫", "기피", "좋아하지"},
					{"정한", "선택", "고른"},
					{"기록", "기억"},
					{"확인", "보고"},
				}},
				{"an owned-device fact is checked before it is quoted", [][]string{
					{"기기", "장비"},
					{"사용자"},
					{"정보", "사실", "설정"},
					{"확인"},
					{"인용", "옮기", "말하", "쓰기"},
				}},
			},
			spoken: []personaRequirement{
				{"the correction example keeps the honorific register", [][]string{
					{"잘못", "실수"},
					{"사옵니다", "하옵니다", "이옵니다"},
				}},
			},
		},
		"creative": {
			name:      "Cleo",
			character: []string{"오타쿠"},
			voice:     []string{"선생", "최애컷", "우효~", "다른 에이전트에게는"},
			core: []personaRequirement{
				{"the favourite is checked against stated taste", [][]string{
					{"최애컷", "최애", "기본안"},
					{"내기 전에", "내기 앞서", "고르기 전에", "제안하기 전에"},
					{"취향"},
					{"맞는지", "맞춤", "부합", "맞나"},
				}},
				{"a hand-off is mentioned only after it is sent", [][]string{
					{"전달", "핸드오프", "handoff"},
					{"보낸 뒤", "보낸 후", "실제로 보낸", "보내고"},
					{"말", "전하", "보고"},
				}},
				{"a reversible rerun is done without asking", [][]string{
					{"되돌릴 수 있는", "되돌릴 수 있"},
					{"재실행", "다시 실행", "재생성", "다시 돌"},
					{"묻지 않고", "묻지 않", "묻지 말고"},
				}},
				{"a vague rejection narrows to one question", [][]string{
					{"막연", "모호"},
					{"거절"},
					{"질문 하나", "한 가지 질문", "하나의 질문", "질문 한"},
					{"좁", "추려"},
				}},
			},
		},
		"investing": {
			name:      "Kai",
			character: []string{"느긋"},
			voice:     []string{"형", "느긋", "다른 에이전트에게는"},
			core: []personaRequirement{
				{"state assumptions and finish instead of ending with questions", [][]string{
					{"가정"},
					{"밝히", "밝혀", "적어", "말해"},
					{"마무리", "끝내", "끝맺"},
					{"질문으로 돌려주지", "질문으로 끝내지", "되묻지", "질문을 남기지"},
				}},
				{"every figure carries a source, an as-of date and a trading day", [][]string{
					{"수치", "숫자", "값"},
					{"출처"},
					{"기준일", "기준 시점", "기준시점"},
					{"종가"},
					{"거래일"},
					{"확인"},
				}},
			},
		},
		"legal": {
			name:      "Kanna",
			character: []string{"고문", "발랄"},
			voice:     []string{"~거든", "~잖아", "다른 에이전트에게는"},
			core: []personaRequirement{
				{"an unasked legal risk is flagged once", [][]string{
					{"묻지 않은", "요청하지 않은", "말하지 않은"},
					{"법적 위험", "법률 위험", "법적 문제"},
					{"한 번", "한번"},
				}},
				{"a stated floor or ceiling is rounded toward", [][]string{
					{"하한", "상한", "최저", "최대"},
					{"반올림", "올림", "내림"},
					{"경계", "한도", "기준"},
					{"지키는", "맞춰", "향해"},
				}},
				{"the person's own document labels are used", [][]string{
					{"항목명", "용어", "명칭", "라벨"},
					{"문서"},
					{"따라", "그대로", "맞춰"},
				}},
			},
		},
	}
}

func TestM4StarterPersonaRevision(t *testing.T) {
	cases := m4Contracts()
	if len(cases) != len(persona.Starters()) {
		t.Fatal("every starter needs a contract case")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			want, ok := cases[starter.Key]
			if !ok || want.name != starter.Name {
				t.Fatalf("starter identity changed: %+v", starter)
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			if heading := firstLine(core); !strings.Contains(heading, starter.Name) {
				t.Errorf("core heading lost the persona name: %q", heading)
			}
			for _, marker := range want.character {
				if !strings.Contains(core, marker) {
					t.Errorf("core lost character marker %q", marker)
				}
			}
			for _, marker := range want.voice {
				if !strings.Contains(voice, marker) {
					t.Errorf("default voice lost voice marker %q", marker)
				}
			}
			for _, requirement := range want.core {
				if !carries(core, requirement.groups) {
					t.Errorf("core lost the M4 change %q", requirement.change)
				}
			}
			for _, requirement := range want.spoken {
				if !carries(voice, requirement.groups) {
					t.Errorf("default voice lost the M4 change %q", requirement.change)
				}
			}
			if lines := exampleLines(voice); lines < 3 {
				t.Errorf("default voice kept only %d examples", lines)
			}
			for layer, body := range map[string]string{"core": core, "default voice": voice} {
				if hits := topicRegisterSwitches(body); len(hits) > 0 {
					t.Errorf("%s switches register by topic: %q", layer, hits)
				}
				if hits := nothingToDecide(body); len(hits) > 0 {
					t.Errorf("%s carries a nothing-to-decide line: %q", layer, hits)
				}
			}
		})
	}
}

// TestM4RequirementMatcher proves the checks above detect a lost change and
// tolerate equivalent wording, so they guard the contract rather than a
// sentence. The rewritten and split fixtures are synthetic.
func TestM4RequirementMatcher(t *testing.T) {
	readSources := personaRequirement{"read sources before asking", [][]string{
		{"미완료", "남은 일", "열일", "할 일"},
		{"나열", "물어", "묻"},
		{"묻기 전에", "묻기 앞서", "물어보기 전에", "물어보기 앞서", "질문하기 전에"},
		{"원출처", "원문", "출처", "원래 자료"},
	}}
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"shipped wording":    {body: "미완료 일을 나열하거나 끝났는지 묻기 전에 원출처를 읽는다.", want: true},
		"rewritten wording":  {body: "할 일을 나열하거나 물어보기 앞서 원문을 먼저 확인한다.", want: true},
		"dropped change":     {body: "궁금한 것은 원문을 먼저 읽고 묻는다.", want: false},
		"concepts separated": {body: "할 일을 나열하지 않는다.\n\n원문을 먼저 읽고 묻는다.", want: false},
		"partly missing":     {body: "남은 일을 나열하기 전에 확인한다.", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := carries(tc.body, readSources.groups); got != tc.want {
				t.Fatalf("carries = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestM4RegisterAndClosingChecks(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		hits int
	}{
		"topic switch":              {body: "건강 이야기에서는 격식체로 바꿔 말한다.", hits: 1},
		"stable voice":              {body: "주제가 무거워도 말투는 그대로다. 안전 내용만 분명히 전한다.", hits: 0},
		"register by sentence role": {body: "결론, 약속과 결정은 합니다체로 말하고 설명은 해요체로 푼다.", hits: 0},
		"correction example":        {body: "소인이 잘못 말씀드렸사옵니다. 다시 확인했사옵니다.", hits: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if hits := topicRegisterSwitches(tc.body); len(hits) != tc.hits {
				t.Fatalf("register hits = %q, want %d", hits, tc.hits)
			}
		})
	}
	for name, tc := range map[string]struct {
		body string
		hits int
	}{
		"korean closing":  {body: "끝났어. 정할 일은 없어.", hits: 1},
		"english closing": {body: "All clear. Nothing to decide now.", hits: 1},
		"open decision":   {body: "남은 결정은 두 가지야.", hits: 0},
		"work still open": {body: "정할 것이 남아 있다.", hits: 0},
		"negated advisal": {body: "결정을 미루라는 말은 하지 않는다.", hits: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if hits := nothingToDecide(tc.body); len(hits) != tc.hits {
				t.Fatalf("closing hits = %q, want %d", hits, tc.hits)
			}
		})
	}
}

var (
	sentenceBreak = regexp.MustCompile(`[.!?。！？\n]+`)
	englishEmpty  = regexp.MustCompile(`(?i)nothing\s+(left\s+)?to\s+decide|no\s+decision\s+to\s+make`)
)

func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		}
	}
	return ""
}

func exampleLines(voice string) int {
	count := 0
	for _, line := range strings.Split(voice, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			count++
		}
	}
	return count
}

// units yields blank-line blocks and their lines, so a rule may be rewritten
// and reflowed while its concepts stay together.
func units(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var out []string
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		out = append(out, block)
		if strings.Contains(block, "\n") {
			for _, line := range strings.Split(block, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					out = append(out, line)
				}
			}
		}
	}
	return out
}

func carries(body string, groups [][]string) bool {
	for _, unit := range units(body) {
		all := true
		for _, group := range groups {
			if !containsAny(unit, group) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func containsAny(text string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}

// topicRegisterSwitches reports sentences that tie a topic to a different
// register. Register chosen by what the sentence does (a conclusion, an
// explanation) is voice and stays allowed, as does restating that the voice does
// not change.
func topicRegisterSwitches(body string) []string {
	topics := []string{"건강", "의료", "결제", "예약", "법률", "법적", "법령", "투자", "장애", "사고", "실수", "민감", "심각", "진지", "잡담", "완료", "구매", "환불"}
	switches := []string{"말투를 바꾸", "말씨를 바꾸", "어투를 바꾸", "말투를 전환", "격식체로", "존댓말로", "반말로", "사극체로", "평어로", "합니다체로", "해요체로", "달리 말한", "달리 쓴"}
	var hits []string
	for _, sentence := range sentenceBreak.Split(body, -1) {
		if containsAny(sentence, topics) && containsAny(sentence, switches) {
			hits = append(hits, strings.TrimSpace(sentence))
		}
	}
	return hits
}

// nothingToDecide reports a decision with nothing left in it. The empty state
// belongs to the Waiting for me view, never to a reply or a persona layer.
func nothingToDecide(body string) []string {
	decisions := []string{"결정", "정할", "판단할"}
	negations := []string{"없다", "없어", "없습니다", "없네", "없음", "없지"}
	var hits []string
	for _, sentence := range sentenceBreak.Split(body, -1) {
		if englishEmpty.MatchString(sentence) || (containsAny(sentence, decisions) && containsAny(sentence, negations)) {
			hits = append(hits, strings.TrimSpace(sentence))
		}
	}
	return hits
}
