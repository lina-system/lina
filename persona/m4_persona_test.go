// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

// The M4 persona revision is specified per preset and layer in
// docs/design/preset-work-methods.md#starter-records-and-persona-changes.
// Check its concrete requirements as well as the revision, identity and voice
// invariants. These text checks do not measure model-response quality (#590).
var m4ChangedLayers = map[string]struct{ core, voice bool }{
	"system": {core: true}, "lina": {core: true}, "dev": {core: true, voice: true},
	"knowledge": {core: true}, "life": {core: true, voice: true}, "creative": {core: true},
	"investing": {core: true}, "legal": {core: true},
}

// Identity markers: the name, the character word the persona is built on and the
// voice's own address terms and register words. The issue fixes these as
// unchanged; they are identity, not phrasing of a judgment rule.
var m4Identity = map[string]struct {
	character []string
	voice     []string
}{
	"system":    {[]string{"귀찮"}, []string{"반말", "~야/~어/~지"}},
	"lina":      {[]string{"무심"}, []string{"주인님", "합니다체", "해요체"}},
	"dev":       {[]string{"차분"}, []string{"마스터", "한다체"}},
	"knowledge": {[]string{"사서"}, []string{"~구먼", "어허"}},
	"life":      {[]string{"생활 큐레이터"}, []string{"소인", "나리", "사극 존대"}},
	"creative":  {[]string{"오타쿠"}, []string{"선생", "최애컷", "우효~"}},
	"investing": {[]string{"느긋"}, []string{"형", "느긋"}},
	"legal":     {[]string{"고문"}, []string{"~거든", "~잖아"}},
}

// Each entry checks one design requirement in its named layer. Groups contain
// alternative expressions of a required concept, not a snapshot of the prose.
// Meaning-preserving rewrites may need new alternatives; they do not require a
// design change. The mutation cases below remove each listed requirement from
// the shipped text independently, including clauses within a shared paragraph.
// Unchanged default voices retain the identity and prohibition checks below.
type m4Change struct {
	change string
	layer  string
	groups [][]string
}

var m4Changes = map[string][]m4Change{
	"system": {
		{"personal tools are not over-guarded", "core", [][]string{
			{"사용자 자신의 도구", "사용자의 도구", "사용자 도구"}, {"자격 증명", "자격증명"}, {"삭제"}, {"비용"}, {"공개"},
			{"지나친", "과도한", "넘어선", "지나치게"}, {"제한", "제약", "통제"},
		}},
		{"an executable step is the agent's", "core", [][]string{
			{"직접 실행", "직접 할", "직접 하는"}, {"사용자에게", "사용자한테"}, {"넘기지", "미루지", "떠넘기지"},
		}},
		{"an all-agent rule is not excepted unasked", "core", [][]string{
			{"모든 에이전트", "전체 에이전트"}, {"규칙"}, {"한 에이전트", "단일 에이전트", "특정 에이전트"}, {"묻", "물어", "확인"},
		}},
	},
	"lina": {
		{"read sources before open items or asking", "core", [][]string{
			{"미완료", "남은 일", "열린 일", "할 일"}, {"나열", "물어", "묻", "질문"},
			{"묻기 전에", "묻기 앞서", "나열하기 전에", "나열하기 앞서", "물어보기 전에", "물어보기 앞서", "질문하기 전에", "질문하기에 앞서"},
			{"원출처", "원문", "출처", "원래 자료"}, {"읽", "열어", "확인"},
		}},
		{"finish a legal draft before review", "core", [][]string{
			{"법", "계약"}, {"초안"}, {"마친 뒤", "마치고", "끝낸 뒤", "완성한 뒤", "마친 다음", "끝낸 다음"}, {"Kanna"}, {"검토", "리뷰"},
		}},
	},
	"dev": {
		{"a peer report is a claim until reproduced", "core", [][]string{
			{"worker", "워커"}, {"다른 에이전트", "동료", "에이전트"}, {"주장"}, {"재현", "다시 실행"}, {"전까지", "때까지", "전에는"},
		}},
		{"done is what ran, not what merged", "core", [][]string{
			{"완료"}, {"병합", "merge", "머지"}, {"실행", "돌려", "run"}, {"아니라", "아닌"},
		}},
		{"evidence labels stay visible", "voice", [][]string{
			{"직접 확인", "직접 검증"}, {"추정", "추측"}, {"미확인", "미검증"},
			{"출처"}, {"구분", "밝히", "표시"}, {"worker"}, {"다른 에이전트", "동료"}, {"보고", "말"},
		}},
	},
	"knowledge": {
		{"one concrete flag instead of warning-led framing", "core", [][]string{
			{"경고부터", "경고를 앞세우지", "경고로 시작", "경고를 먼저"}, {"위험", "우려"}, {"하나", "한 가지"},
			{"구체", "분명", "특정"}, {"한 번", "한번"},
		}},
		{"read a source or record before asking", "core", [][]string{
			{"묻기 전에", "묻기 앞서", "질문하기 전에", "물어보기 전에", "질문하기에 앞서"}, {"읽", "봐", "확인"}, {"출처", "기록", "자료"},
		}},
	},
	"life": {
		{"same voice on every topic with explicit safety", "core", [][]string{
			{"주제", "화제"}, {"말투", "말씨", "목소리"}, {"그대로", "같은", "유지", "바꾸지"}, {"안전"}, {"분명", "명확", "또렷"},
		}},
		{"recorded dislikes and choices come before a recommendation", "core", [][]string{
			{"추천 전에", "권하기 전에", "권하기 앞서"}, {"싫", "기피", "좋아하지"}, {"정한", "선택", "고른"},
			{"기록", "기억"}, {"확인", "보고"},
		}},
		{"an owned-device fact is checked before it is quoted", "core", [][]string{
			{"기기", "장비"}, {"사용자"}, {"정보", "사실", "설정"}, {"확인한 뒤", "확인하고", "확인 후"}, {"인용", "옮기", "말하", "쓰기"},
		}},
		{"the correction example keeps the honorific register", "voice", [][]string{
			{"잘못", "실수"}, {"사옵니다", "하옵니다", "이옵니다"},
		}},
	},
	"creative": {
		{"the favourite is checked against stated taste", "core", [][]string{
			{"최애컷", "최애", "기본안"}, {"내기 전에", "내기 앞서", "고르기 전에", "제안하기 전에"}, {"취향"}, {"맞는지", "맞춤", "부합", "맞나"},
		}},
		{"a hand-off is mentioned only after it is sent", "core", [][]string{
			{"전달", "핸드오프", "handoff"}, {"보낸 뒤", "보낸 후", "실제로 보낸", "보내고"}, {"말", "전하", "보고"},
		}},
		{"a reversible rerun is done without asking", "core", [][]string{
			{"되돌릴 수 있는", "되돌릴 수 있"}, {"재실행", "다시 실행", "재생성", "다시 돌"}, {"묻지 않고", "묻지 않", "묻지 말고"},
		}},
		{"a vague rejection narrows to one question", "core", [][]string{
			{"막연", "모호"}, {"거절"}, {"질문 하나", "한 가지 질문", "하나의 질문", "질문 한"}, {"좁", "추려"},
		}},
	},
	"investing": {
		{"state assumptions and finish instead of ending with questions", "core", [][]string{
			{"가정"}, {"밝히", "밝혀", "적어", "말해"}, {"마무리", "끝내", "끝맺"},
			{"질문으로 돌려주지", "질문으로 끝내지", "되묻지", "질문을 남기지"},
		}},
		{"every figure carries a source and an as-of date", "core", [][]string{
			{"수치", "숫자", "값"}, {"출처"}, {"기준일", "기준 시점", "기준시점"},
		}},
		{"a quoted close is checked against the trading day", "core", [][]string{
			{"종가"}, {"거래일"}, {"확인"},
		}},
	},
	"legal": {
		{"an unasked legal risk is flagged once", "core", [][]string{
			{"묻지 않은", "요청하지 않은", "말하지 않은"}, {"법적 위험", "법률 위험", "법적 문제"}, {"한 번", "한번"},
		}},
		{"a stated floor or ceiling is rounded toward", "core", [][]string{
			{"사용자", "사람"}, {"정한", "명시한"}, {"하한", "최저"}, {"상한", "최대"}, {"반올림", "올림", "내림"}, {"경계", "한도", "기준"}, {"지키는", "맞춰", "향해"},
		}},
		{"the person's own document labels are used", "core", [][]string{
			{"항목명", "용어", "명칭", "라벨"}, {"사용자 자신의 문서", "사용자의 문서"}, {"따라", "그대로", "맞춰"},
		}},
	},
}

func TestM4StarterChangesAreCarried(t *testing.T) {
	if len(m4Changes) != len(persona.Starters()) {
		t.Fatal("every starter needs its listed changes")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			changes, ok := m4Changes[starter.Key]
			if !ok || len(changes) == 0 {
				t.Fatalf("starter %s has no listed change", starter.Key)
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			layers := map[string]string{"core": core, "voice": voice}
			for _, change := range changes {
				body, ok := layers[change.layer]
				if !ok {
					t.Fatalf("change %q names an unknown layer %q", change.change, change.layer)
				}
				if !carriesChange(body, change) {
					t.Errorf("%s layer does not carry the listed change %q", change.layer, change.change)
				}
			}
		})
	}
}

// These are removal fixtures, not required prose. Each row corresponds to the
// design requirement at the same index in m4Changes; each fragment is removed
// independently. Keep other changes in the paragraph, so an unrelated addition
// cannot hide the regression. Updating wording also updates its removal fixture.
func TestM4EachDocumentedChangeIsRequired(t *testing.T) {
	removals := map[string][][]string{
		"system": {
			{"사용자 자신의 도구에는 자격 증명, 삭제, 비용과 공개의 경계를 넘어 지나친 제한을 두지 않는다.", "자격 증명, ", "삭제, ", "비용과 ", "공개의 "},
			{"내가 직접 실행할 수 있는 단계는 사용자에게 넘기지 않는다."},
			{"사용자가 모든 에이전트에 정한 규칙을 한 에이전트만 예외로 바꾸려면 먼저 묻는다."},
		},
		"lina": {
			{"미완료 일을 나열하거나 끝났는지 묻기 전에 원출처를 읽는다."},
			{"법과 계약에 걸친 문서도 맡은 초안은 직접 마친 뒤 Kanna에게 검토를 맡긴다."},
		},
		"dev": {
			{"worker나 다른 에이전트의 보고는 직접 재현하기 전까지 그들의 주장으로 남긴다."},
			{"완료는 병합한 것이 아니라 실제로 실행해 확인한 것에만 쓴다."},
			{"직접 확인, 추정, 미확인은 출처가 보이게 구분한다.", "다른 에이전트나 worker의 말은 누구의 보고인지 밝힌다.", "직접 확인, ", "추정, ", "미확인은 "},
		},
		"knowledge": {
			{"경고부터 앞세우지 않고 필요한 위험 하나를 구체적으로 한 번 짚어.", "하나를 ", "구체적으로 ", "한 번 "},
			{"사용자에게 묻기 전에 읽을 수 있는 출처나 기존 기록부터 봐."},
		},
		"life": {
			{"주제가 무겁거나 건강에 관한 이야기여도 말투는 그대로이옵니다.", "안전에 필요한 내용은 같은 말투로 분명하게 전하옵니다."},
			{"추천 전에 사용자가 싫다고 하신 것과 이미 정한 선택을 기록에서 확인하옵니다.", "싫다고 하신 것과 ", "이미 정한 선택을 "},
			{"사용자 자신의 기기 정보도 확인한 뒤 인용하옵니다."},
			{"소인이 잘못 말씀드렸사옵니다. 시작은 19시가 아니라 18시 30분이옵니다. 예매 화면에서 다시 확인했고 동선도 고쳤사옵니다."},
		},
		"creative": {
			{"최애컷을 내기 전에 사용자가 말한 취향과 맞는지 봐."},
			{"다른 에이전트에게 전달했다는 말은 실제로 보낸 뒤에만 해."},
			{"되돌릴 수 있는 재실행은 묻지 않고 해."},
			{"막연한 거절 뒤에는 결과를 바꿀 질문 하나로 좁혀."},
		},
		"investing": {
			{"판단에 쓴 가정을 밝히고 답을 마무리해.", "답할 수 있는 일을 끝맺는 질문으로 돌려주지 않아."},
			{"인용하는 모든 수치에는 출처와 기준일을 붙여.", "출처와 ", "기준일을 "},
			{"종가는 해당 날짜가 실제 거래일인지 확인해."},
		},
		"legal": {
			{"사용자가 묻지 않은 법적 위험도 한 번 짚어."},
			{"사용자가 정한 하한이나 상한이 있는 수치는 그 경계를 지키는 방향으로 반올림해.", "하한이나 ", "상한이 "},
			{"문서의 항목명은 사용자 자신의 문서에 쓰인 것을 따라."},
		},
	}
	if len(removals) != len(m4Changes) {
		t.Fatal("every preset needs removal fixtures")
	}
	for key, changes := range m4Changes {
		t.Run(key, func(t *testing.T) {
			if len(removals[key]) != len(changes) {
				t.Fatal("every listed change needs removal fixtures")
			}
			core, voice, err := persona.Load(persona.Shipped(), key)
			if err != nil {
				t.Fatal(err)
			}
			for i, change := range changes {
				t.Run(change.layer+"/"+change.change, func(t *testing.T) {
					body := map[string]string{"core": core, "voice": voice}[change.layer]
					if !carriesChange(body, change) {
						t.Fatal("shipped layer does not carry the requirement")
					}
					if len(removals[key][i]) == 0 {
						t.Fatal("missing removal fixture")
					}
					for _, fragment := range removals[key][i] {
						if !strings.Contains(body, fragment) {
							t.Fatalf("update stale removal fixture %q", fragment)
						}
						mutated := strings.ReplaceAll(body, fragment, "")
						if carriesChange(mutated, change) {
							t.Errorf("requirement still passes after removing %q", fragment)
						}
					}
				})
			}
		})
	}
}

func TestM4StarterRevisionCarriesTheDocumentedChanges(t *testing.T) {
	if len(m4ChangedLayers) != len(persona.Starters()) {
		t.Fatal("every starter needs a ledger entry")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			want, ok := m4ChangedLayers[starter.Key]
			if !ok {
				t.Fatalf("starter %s has no ledger entry", starter.Key)
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			checkRevision(t, "core", core, baseline(t, starter.Key, "core-v1.md"))
			if want.voice {
				checkRevision(t, "default voice", voice, baseline(t, starter.Key, "default-voice-v1.md"))
			}
		})
	}
}

func TestM4StarterIdentityAndVoice(t *testing.T) {
	cases := m4Identity
	if len(cases) != len(persona.Starters()) {
		t.Fatal("every starter needs identity markers")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			want, ok := cases[starter.Key]
			if !ok {
				t.Fatalf("starter %s has no markers", starter.Key)
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			if title := titleOf(core); !strings.Contains(title, starter.Name) {
				t.Errorf("core title lost the persona name: %q", title)
			}
			if title := titleOf(voice); !strings.Contains(title, starter.Name) {
				t.Errorf("default voice title lost the persona name: %q", title)
			}
			for _, marker := range want.character {
				if !strings.Contains(core, marker) {
					t.Errorf("core lost the character marker %q", marker)
				}
			}
			for _, marker := range want.voice {
				if !strings.Contains(voice, marker) {
					t.Errorf("default voice lost the voice marker %q", marker)
				}
			}
			if examples(voice) < 4 {
				t.Errorf("default voice kept %d examples", examples(voice))
			}
			if !strings.Contains(voice, "다른 에이전트에게는") {
				t.Error("default voice lost the line for agent exchanges")
			}
		})
	}
}

// TestM4StarterLayersHaveNoTopicSwitchOrClosing holds the revision's two
// prohibitions: no layer may tie a register to a topic, and none may end on a
// fixed nothing-to-decide line. Both are patterns over how a sentence is built,
// not a required wording.
func TestM4StarterLayersHaveNoTopicSwitchOrClosing(t *testing.T) {
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
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

// The checks are only worth their green if a regression fails them. These
// synthetic fixtures cover both directions for every helper the tests use.
func TestM4ChecksCatchRegressions(t *testing.T) {
	baselineLayer := "title\n\nrule one\n\nrule two\n\nrule three\n\nrule four"
	for name, tc := range map[string]struct {
		body     string
		problems int
	}{
		"unchanged":            {body: baselineLayer, problems: 1},
		"reworded rule":        {body: "title\n\nrule ONE, said differently\n\nrule two\n\nrule three\n\nrule four", problems: 0},
		"added rule":           {body: baselineLayer + "\n\nrule five", problems: 0},
		"dropped rule":         {body: "title\n\nrule one\n\nrule two\n\nrule three", problems: 2},
		"dropped and replaced": {body: "title\n\nrule one\n\nrule two\n\nrule three\n\nrule five", problems: 0},
		"example in the core":  {body: baselineLayer + "\n\n- an example", problems: 1},
		"stubbed core":         {body: "title\n\nrule one", problems: 3},
	} {
		t.Run(name, func(t *testing.T) {
			if got := revisionProblems("core", tc.body, baselineLayer); len(got) != tc.problems {
				t.Fatalf("problems = %q, want %d", got, tc.problems)
			}
		})
	}
	for name, tc := range map[string]struct {
		body string
		hits int
	}{
		"topic switch":              {body: "건강 이야기에서는 격식체로 바꿔 말한다.", hits: 1},
		"stable voice":              {body: "주제가 무거워도 말투는 그대로다. 안전 내용만 분명히 전한다.", hits: 0},
		"register by sentence role": {body: "결론, 약속과 결정은 합니다체로 말하고 설명은 해요체로 푼다.", hits: 0},
		"correction example":        {body: "소인이 잘못 말씀드렸사옵니다. 다시 확인했사옵니다.", hits: 0},
		"forbidden switch":          {body: "건강, 결제와 예약, 완료 보고와 실수처럼 정확해야 하는 순간에도 말투를 바꾸지 않고 사극 존대를 그대로 쓰며 필요한 내용을 또렷하게 전하옵니다.", hits: 0},
		"topic switch per topic":    {body: "건강 이야기는 격식체로, 잡담은 반말로 쓴다.", hits: 1},
	} {
		t.Run("register/"+name, func(t *testing.T) {
			if hits := topicRegisterSwitches(tc.body); len(hits) != tc.hits {
				t.Fatalf("register hits = %q, want %d", hits, tc.hits)
			}
		})
	}
	for name, tc := range map[string]struct {
		body string
		hits int
	}{
		"korean closing":             {body: "끝났어. 정할 일은 없어.", hits: 1},
		"english closing":            {body: "All clear. Nothing to decide now.", hits: 1},
		"open decision":              {body: "남은 결정은 두 가지야.", hits: 0},
		"work still open":            {body: "정할 것이 남아 있다.", hits: 0},
		"negated advisal":            {body: "결정을 미루라는 말은 하지 않는다.", hits: 0},
		"forbidden closing":          {body: "정할 일이 없다는 고정 문구로 답변을 끝내지 않는다.", hits: 0},
		"forbidden closing, english": {body: "Do not end a reply with nothing to decide.", hits: 0},
	} {
		t.Run("closing/"+name, func(t *testing.T) {
			if hits := nothingToDecide(tc.body); len(hits) != tc.hits {
				t.Fatalf("closing hits = %q, want %d", hits, tc.hits)
			}
		})
	}
	groups := m4Changes["lina"][0].groups
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"wording accepted":  {body: "미완료 일을 나열하기 전에 원출처를 읽는다.", want: true},
		"rule dropped":      {body: "미완료 일을 나열한다.", want: false},
		"rule split":        {body: "미완료 일을 나열하기 전에 확인한다.\n\n원출처를 읽는다.", want: false},
		"previous revision": {body: strings.Join(baseline(t, "lina", "core-v1.md"), "\n\n"), want: false},
	} {
		t.Run("changes/"+name, func(t *testing.T) {
			if got := carries(tc.body, groups); got != tc.want {
				t.Fatalf("carries = %v, want %v", got, tc.want)
			}
		})
	}
}

// Voice correction requirements must be satisfied by the quoted correction,
// not its caption or a nearby register rule. Other requirements belong in rule
// paragraphs: examples must not conceal a missing core or voice instruction.
func carriesChange(body string, change m4Change) bool {
	if change.change == "the correction example keeps the honorific register" {
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "- ") {
				continue
			}
			_, quote, ok := strings.Cut(line, "“")
			if ok && carries(quote, change.groups) {
				return true
			}
		}
		return false
	}
	var rules []string
	for _, paragraph := range paragraphs(body) {
		if !strings.Contains(paragraph, "\n- ") && !strings.HasPrefix(paragraph, "- ") &&
			!strings.HasPrefix(paragraph, "#") && !strings.HasPrefix(paragraph, "<!--") {
			rules = append(rules, paragraph)
		}
	}
	if change.change == "every figure carries a source and an as-of date" {
		// A source mentioned elsewhere in the paragraph cannot label a figure.
		for _, sentence := range sentenceBreak.Split(strings.Join(rules, "\n\n"), -1) {
			if carries(sentence, change.groups) {
				return true
			}
		}
		return false
	}
	return carries(strings.Join(rules, "\n\n"), change.groups)
}

// carries reports whether one paragraph (or quoted example line) of a layer
// states every group of a listed change.
func carries(body string, groups [][]string) bool {
	for _, unit := range units(body) {
		ok := true
		for _, group := range groups {
			if !containsAny(unit, group) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// units are the layer's paragraphs and, for the default voice, its quoted
// example lines. A rule must sit in one unit; two fragments in different
// paragraphs are two different rules.
func units(body string) []string {
	out := paragraphs(body)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-") {
			out = append(out, line)
		}
	}
	return out
}

// checkRevision requires a documented change to exist as a rule the previous
// revision did not carry, and requires every removed paragraph to be replaced.
// It never compares the new rule's words with anything.
func checkRevision(t *testing.T, layer, body string, baseline []string) {
	t.Helper()
	for _, problem := range revisionProblems(layer, body, strings.Join(baseline, "\n\n")) {
		t.Errorf("%s %s", layer, problem)
	}
}

func revisionProblems(layer, body, baseline string) []string {
	previous := map[string]bool{}
	for _, paragraph := range paragraphs(baseline) {
		previous[paragraph] = true
	}
	current := paragraphs(body)
	present := map[string]bool{}
	var problems []string
	added, removed := 0, 0
	for _, paragraph := range current {
		present[paragraph] = true
		if !previous[paragraph] {
			added++
			if layer == "core" && strings.HasPrefix(paragraph, "- ") {
				problems = append(problems, "landed a core change in an example")
			}
		}
	}
	for _, paragraph := range paragraphs(baseline) {
		if !present[paragraph] {
			removed++
		}
	}
	if added == 0 {
		problems = append(problems, "equals the #584 revision; the design lists a change for it")
	}
	if removed > added {
		problems = append(problems, "dropped a paragraph without replacing it")
	}
	if count := rules(current); count < 4 {
		problems = append(problems, "kept too few rule paragraphs")
	}
	return problems
}

func baseline(t *testing.T, key, layer string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "m4-baseline", key, layer))
	if err != nil {
		t.Fatal(err)
	}
	return paragraphs(string(data))
}

func paragraphs(body string) []string {
	var out []string
	for _, paragraph := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			out = append(out, paragraph)
		}
	}
	return out
}

func titleOf(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		}
	}
	return ""
}

func examples(voice string) int {
	count := 0
	for _, line := range strings.Split(voice, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			count++
		}
	}
	return count
}

// rules counts the core paragraphs that state how the agent judges: not the
// title, the licence line, an example or the line for agent exchanges.
func rules(current []string) int {
	count := 0
	for _, paragraph := range current {
		if !strings.HasPrefix(paragraph, "#") && !strings.HasPrefix(paragraph, "<!--") &&
			!strings.HasPrefix(paragraph, "- ") && !strings.Contains(paragraph, "다른 에이전트에게는") {
			count++
		}
	}
	return count
}

var (
	sentenceBreak   = regexp.MustCompile(`[.!?。！？\n]+`)
	englishEmpty    = regexp.MustCompile(`(?i)nothing\s+(left\s+)?to\s+decide|no\s+decision\s+to\s+make`)
	englishNegation = regexp.MustCompile(`(?i)\b(do\s+not|don't|never|avoid|no\s+need\s+to)\b`)
	// "없" states the emptiness; "없이" ("without") is a negation and no claim.
	koreanEmptiness = regexp.MustCompile(`없(?:[^이]|$)`)
)

// topicRegisterSwitches reports sentences that instruct the agent to use a
// different register on some topic. Register chosen by what a sentence does (a
// conclusion, an explanation) is voice and stays allowed, and so is a sentence
// that forbids the switch: the check looks for an instruction to do it.
func topicRegisterSwitches(body string) []string {
	topics := []string{"건강", "의료", "결제", "예약", "법률", "법적", "법령", "투자", "장애", "사고", "실수", "민감", "심각", "진지", "잡담", "완료", "구매", "환불"}
	switches := []string{"말투를 바꾸", "말씨를 바꾸", "어투를 바꾸", "말투를 전환", "격식체로", "존댓말로", "반말로", "사극체로", "평어로", "합니다체로", "해요체로", "달리 말한", "달리 쓴"}
	var hits []string
	for _, sentence := range sentenceBreak.Split(body, -1) {
		if containsAny(sentence, topics) && containsAny(sentence, switches) && instructed(sentence) {
			hits = append(hits, strings.TrimSpace(sentence))
		}
	}
	return hits
}

// nothingToDecide reports a sentence that tells the agent to close on a
// decision with nothing left in it. The empty state belongs to the Waiting for
// me view, never to a reply or a persona layer.
func nothingToDecide(body string) []string {
	decisions := []string{"결정", "정할", "판단할"}
	var hits []string
	for _, sentence := range sentenceBreak.Split(body, -1) {
		if englishEmpty.MatchString(sentence) && instructed(sentence) ||
			containsAny(sentence, decisions) && koreanEmptiness.MatchString(sentence) && instructed(sentence) {
			hits = append(hits, strings.TrimSpace(sentence))
		}
	}
	return hits
}

// instructed reports whether a sentence asks for something rather than
// forbidding it. A layer may state the prohibition itself ("do not switch
// register", "do not end with a nothing-to-decide line"), so a negated predicate
// carries no violation. "없" is not a negation here: it is the emptiness the
// closing line states.
func instructed(sentence string) bool {
	return !containsAny(sentence, []string{"않", "말고", "금지", "마라", "말아", "아니라", "대신", "배제"}) &&
		!englishNegation.MatchString(sentence)
}

func containsAny(text string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}
