// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"io/fs"
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
// what the design still requires of the shipped layers, in structural terms:
// the revision files and their sections, the two voice prohibitions, and the
// identity markers the issue fixes. They pin no rule wording, because a required
// phrase fails a legitimate rewrite; content review and the #590 replay own the
// meaning of the text, and model-response quality is #590's subject.

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

// TestM4StarterLayersAreShippedRevisions checks the structure of a shipped pair:
// both versioned revision files are present in the preset source, each layer has
// one title heading, the core keeps its rule paragraphs and the default voice
// keeps its examples. A rewrite that keeps that shape passes; a layer reduced to
// a title, or a missing revision file, fails.
func TestM4StarterLayersAreShippedRevisions(t *testing.T) {
	if len(m4Identity) != len(persona.Starters()) {
		t.Fatal("every starter needs identity markers")
	}
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			for _, name := range []string{"core-v1.md", "default-voice-v1.md"} {
				info, err := fs.Stat(persona.Shipped(), starter.Key+"/"+name)
				if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
					t.Fatalf("preset %s does not ship a usable %s revision", starter.Key, name)
				}
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			for layer, body := range map[string]string{"core": core, "default voice": voice} {
				if title := titleOf(body); title == "" {
					t.Errorf("%s has no title heading", layer)
				}
				if count := headings(body); count != 1 {
					t.Errorf("%s has %d title headings, want 1", layer, count)
				}
			}
			if count := rules(core); count < 4 {
				t.Errorf("core keeps %d rule paragraphs, want at least 4", count)
			}
			if count := examples(voice); count < 4 {
				t.Errorf("default voice keeps %d examples, want at least 4", count)
			}
		})
	}
}

// TestM4StarterIdentityAndVoice holds the markers the issue fixes: the persona
// name in both titles, the character word in the core and the voice's address
// terms and register words in the default voice. A layer may reword its rules
// and examples without failing; dropping one of these markers fails.
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

// headings counts the layer's top-level title headings.
func headings(body string) int {
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			count++
		}
	}
	return count
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
func rules(core string) int {
	count := 0
	for _, paragraph := range strings.Split(strings.ReplaceAll(core, "\r\n", "\n"), "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" || strings.HasPrefix(paragraph, "#") ||
			strings.HasPrefix(paragraph, "<!--") || strings.HasPrefix(paragraph, "- ") {
			continue
		}
		count++
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
