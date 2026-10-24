// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

// The M4 persona revision is specified per preset in
// docs/design/preset-work-methods.md#starter-records-and-persona-changes.
// The M4-4 replay (#590) rejected the persona-layer changes it measured, so the
// shipped layers keep the #584 conversion's text; only the life default voice
// ships the register correction the presets contract requires. These tests hold
// what the design still requires of the shipped layers: the two voice
// prohibitions and the identity markers the issue fixes. They say nothing about
// model-response quality (#590), and they do not pin the rule wording, because
// the design does not ship the rule changes.
//
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

func TestM4StarterIdentityAndVoice(t *testing.T) {
	if len(m4Identity) != len(persona.Starters()) {
		t.Fatal("every starter needs identity markers")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			want, ok := m4Identity[starter.Key]
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

// TestM4StarterLayersHaveNoTopicSwitchOrClosing holds the two prohibitions the
// presets contract puts on a persona layer: no layer may tie a register to a
// topic, and none may close on a decision with nothing left in it. Both are
// patterns over how a sentence is built, not a required wording.
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

// TestM4ArinCorrectionExampleKeepsTheVoiceRegister holds the one persona-layer
// change the design still requires: the correction example stays in the
// layer's own register. The register is derived from the layer's other
// examples, so rewriting the whole voice keeps passing while dropping one
// example into another speech level fails.
func TestM4ArinCorrectionExampleKeepsTheVoiceRegister(t *testing.T) {
	_, voice, err := persona.Load(persona.Shipped(), "life")
	if err != nil {
		t.Fatal(err)
	}
	correction, others := correctionExample(voice)
	if correction == "" {
		t.Fatal("default voice lost its correction example")
	}
	want := majorityRegister(others)
	if got := registerClass(correction); got != want {
		t.Errorf("correction example is %s while the other examples are %s: %q", got, want, correction)
	}
	for name, tc := range map[string]struct {
		voice      string
		wantOthers string
		wantCorr   string
	}{
		"honorific correction": {
			voice:      "- a: “소인이 잘못 말씀드렸사옵니다.”\n- b: “권하옵니다.”\n- c: “맞사옵니다.”",
			wantOthers: "honorific", wantCorr: "honorific",
		},
		"plain correction among honorific": {
			voice:      "- a: “제가 잘못 말씀드렸습니다.”\n- b: “권하옵니다.”\n- c: “맞사옵니다.”",
			wantOthers: "honorific", wantCorr: "plain",
		},
		"uniform rewrite": {
			voice:      "- a: “제가 잘못 말씀드렸습니다.”\n- b: “권합니다.”\n- c: “맞습니다.”",
			wantOthers: "plain", wantCorr: "plain",
		},
	} {
		t.Run(name, func(t *testing.T) {
			correction, others := correctionExample(tc.voice)
			if got := majorityRegister(others); got != tc.wantOthers {
				t.Fatalf("other examples are %s, want %s", got, tc.wantOthers)
			}
			if got := registerClass(correction); got != tc.wantCorr {
				t.Errorf("correction example is %s, want %s", got, tc.wantCorr)
			}
		})
	}
}

// The checks are only worth their green if a violation fails them. These
// synthetic fixtures cover both directions for every helper the tests use.
func TestM4ChecksCatchRegressions(t *testing.T) {
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

// correctionExample returns the quoted text of the example that reports a
// mistake, and the quoted text of every other example.
func correctionExample(voice string) (string, []string) {
	var correction string
	var others []string
	for _, line := range strings.Split(voice, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		_, quote, ok := strings.Cut(line, "“")
		if !ok {
			continue
		}
		if strings.Contains(quote, "잘못") && correction == "" {
			correction = quote
			continue
		}
		others = append(others, quote)
	}
	return correction, others
}

// registerClass names the speech level of one quoted example. A layer's
// register is a property of how its examples end, not a fixed sentence: an
// example using the historical honorific endings is "honorific", one using the
// plain formal endings is "plain", and one mixing both is "mixed".
func registerClass(quote string) string {
	honorific := containsAny(quote, []string{"옵니다", "옵소서", "사옵", "이옵", "하옵"})
	plain := containsAny(quote, []string{"합니다", "입니다", "습니다"})
	switch {
	case honorific && plain:
		return "mixed"
	case honorific:
		return "honorific"
	case plain:
		return "plain"
	default:
		return "plain"
	}
}

// majorityRegister returns the register most of the examples use, so a layer
// that rewrites every example at once keeps passing. A tie goes to the first
// example's register.
func majorityRegister(quotes []string) string {
	counts := map[string]int{}
	first := "plain"
	for _, quote := range quotes {
		class := registerClass(quote)
		if counts[class] == 0 && len(counts) == 0 {
			first = class
		}
		counts[class]++
	}
	best := first
	for class, count := range counts {
		if count > counts[best] {
			best = class
		}
	}
	return best
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
