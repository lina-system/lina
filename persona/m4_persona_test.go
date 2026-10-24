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

// The M4 persona revision (docs/design/preset-work-methods.md, Starter records
// and persona changes) gives every starter a core-layer change and changes the
// default voice only for dev and life. The tests below check that revision
// structurally. Each pair is compared with the layer the #584 conversion shipped
// (persona/testdata/m4-baseline), so a reworded rule still passes while a rule
// that was dropped or reverted fails. Judgments about what the prose means are
// reviewed in evidence/persona-conversion.md and measured by the M4-4 replay
// (#590); no test here decides meaning from a phrase list. A partial edit inside
// an already changed paragraph is outside what a structural check can see, and
// belongs to that review and the replay.
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

// The changes each preset is listed under, transcribed from
// docs/design/preset-work-methods.md#starter-records-and-persona-changes. A
// change is carried when one paragraph (or example line) of the named layer
// matches every group, and each group lists the wordings that state that part of
// the rule. The list exists because the issue requires the test to check each
// preset against the design; it is a list of the contract's terms, and a rewrite
// that drops one of them is a different rule. Presets the design lists as
// "Default voice: unchanged" have no voice entry: their voice is verified by the
// identity, example and prohibition checks below instead of by its wording.
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
			{"원출처", "원문", "출처", "원래 자료"},
		}},
		{"finish a legal draft before review", "core", [][]string{
			{"초안"}, {"마친 뒤", "마치고", "끝낸 뒤", "완성한 뒤", "마친 다음", "끝낸 다음"}, {"Kanna"}, {"검토", "리뷰"},
		}},
	},
	"dev": {
		{"a peer report is a claim until reproduced", "core", [][]string{
			{"worker", "워커"}, {"다른 에이전트", "동료", "에이전트"}, {"주장"}, {"재현", "확인"}, {"전까지", "때까지", "전에는"},
		}},
		{"done is what ran, not what merged", "core", [][]string{
			{"완료"}, {"병합", "merge", "머지"}, {"실행", "돌려", "run"}, {"아니라", "아닌"},
		}},
		{"evidence labels stay visible", "voice", [][]string{
			{"출처"}, {"구분", "밝히", "표시"}, {"worker", "다른 에이전트"}, {"보고", "말"},
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
			{"기기", "장비"}, {"사용자"}, {"정보", "사실", "설정"}, {"확인"}, {"인용", "옮기", "말하", "쓰기"},
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
		{"every figure carries a source, an as-of date and a trading day", "core", [][]string{
			{"수치", "숫자", "값"}, {"출처"}, {"기준일", "기준 시점", "기준시점"}, {"종가"}, {"거래일"}, {"확인"},
		}},
	},
	"legal": {
		{"an unasked legal risk is flagged once", "core", [][]string{
			{"묻지 않은", "요청하지 않은", "말하지 않은"}, {"법적 위험", "법률 위험", "법적 문제"}, {"한 번", "한번"},
		}},
		{"a stated floor or ceiling is rounded toward", "core", [][]string{
			{"하한", "상한", "최저", "최대"}, {"반올림", "올림", "내림"}, {"경계", "한도", "기준"}, {"지키는", "맞춰", "향해"},
		}},
		{"the person's own document labels are used", "core", [][]string{
			{"항목명", "용어", "명칭", "라벨"}, {"문서"}, {"따라", "그대로", "맞춰"},
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
				if !carries(body, change.groups) {
					t.Errorf("%s layer does not carry the listed change %q", change.layer, change.change)
				}
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
