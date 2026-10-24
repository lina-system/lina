// SPDX-License-Identifier: Apache-2.0

package persona_test

import (
	"io/fs"
	"strings"
	"testing"
	"unicode"

	"github.com/thisisjun786/lina/persona"
)

// The M4 persona revision is specified per preset in
// docs/design/preset-work-methods.md#starter-records-and-persona-changes.
// The M4-4 replay (#590) rejected the persona-layer changes it measured, so the
// shipped layers keep the #584 conversion's text; only the life default voice
// ships the register correction the presets contract requires. These tests hold
// what the design still requires of the shipped layers, in structural and
// token form: the revision files and their sections, the two voice prohibitions,
// and the identity markers the issue fixes. No assertion depends on a phrase or
// on the separators a layer chooses; content review and the #590 replay own the
// meaning of the text, and model-response quality is #590's subject.

// Identity markers, one token each: the persona's names, the character words the
// persona is built on, and the voice's own address terms, register words and
// speech endings. The issue fixes these as unchanged. A token is checked after
// punctuation and whitespace are stripped, so "~야/~어/~지" and "~야, ~어, ~지"
// are the same three tokens.
var m4Identity = map[string]struct {
	core  []string
	voice []string
}{
	"system": {
		core:  []string{"Selene", "셀레네", "발렌티누스", "귀찮"},
		voice: []string{"Selene", "반말", "야", "어", "지"},
	},
	"lina": {
		core:  []string{"Lina", "리나", "테미스", "무심"},
		voice: []string{"Lina", "주인님", "합니다체", "해요체"},
	},
	"dev": {
		core:  []string{"Sion", "시온", "레토", "차분"},
		voice: []string{"Sion", "마스터", "한다체"},
	},
	"knowledge": {
		core:  []string{"Rumi", "루미", "레토", "사서"},
		voice: []string{"Rumi", "구먼", "어허"},
	},
	"life": {
		core:  []string{"Arin", "아린", "헤스티", "생활", "큐레이터"},
		voice: []string{"Arin", "소인", "나리", "사극", "존대"},
	},
	"creative": {
		core:  []string{"Cleo", "클레오", "아스터", "오타쿠"},
		voice: []string{"Cleo", "선생", "최애컷", "우효"},
	},
	"investing": {
		core:  []string{"Kai", "카이", "테미스", "느긋"},
		voice: []string{"Kai", "형", "느긋"},
	},
	"legal": {
		core:  []string{"Kanna", "칸나", "카델", "고문"},
		voice: []string{"Kanna", "거든", "잖아"},
	},
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

// TestM4StarterIdentityAndVoice holds the markers the issue fixes, one token at
// a time: the persona's names, its character words and the voice's address
// terms, register words and speech endings. A layer may reword its rules,
// reorder its markers or change their separators; dropping a token fails.
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
			for _, token := range want.core {
				if !hasToken(core, token) {
					t.Errorf("core lost the marker %q", token)
				}
			}
			for _, token := range want.voice {
				if !hasToken(voice, token) {
					t.Errorf("default voice lost the marker %q", token)
				}
			}
		})
	}
}

// TestM4StarterLayersHaveNoTopicSwitchOrClosing holds the two prohibitions the
// presets contract puts on a persona layer: no layer may tie a register to a
// topic, and none may close on a decision with nothing left in it. Both are
// patterns over the tokens a sentence is built from, not a required wording.
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
		"switch without spaces":     {body: "건강이야기에서는격식체로바꿔말한다", hits: 1},
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
	for name, tc := range map[string]struct {
		body  string
		token string
		want  bool
	}{
		"markers with slashes":  {body: "기본 어미는 “~야/~어/~지”.", token: "~어", want: true},
		"markers with commas":   {body: "기본 어미는 “~야, ~어, ~지”.", token: "~어", want: true},
		"markers without tilde": {body: "기본 어미는 “야 어 지”.", token: "~어", want: true},
		"marker missing":        {body: "기본 종결은 “~야, ~지”.", token: "~어", want: false},
		"name with particle":    {body: "Selene의 기본 말투", token: "Selene", want: true},
		"name missing":          {body: "기본 말투", token: "Selene", want: false},
	} {
		t.Run("token/"+name, func(t *testing.T) {
			if got := hasToken(tc.body, tc.token); got != tc.want {
				t.Fatalf("hasToken(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

// hasToken reports whether a layer carries a marker token. Punctuation and
// whitespace are stripped from both sides, so separators, particles and line
// breaks do not decide the result.
func hasToken(body, token string) bool {
	token = normalize(token)
	return token != "" && strings.Contains(normalize(body), token)
}

// normalize strips whitespace, punctuation and symbols, leaving the letters and
// digits a marker is made of.
func normalize(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, text)
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

// topicRegisterSwitches reports sentences that instruct the agent to use a
// different register on some topic. Register chosen by what a sentence does (a
// conclusion, an explanation) is voice and stays allowed, and so is a sentence
// that forbids the switch: the check looks for an instruction to do it.
func topicRegisterSwitches(body string) []string {
	topics := []string{"건강", "의료", "결제", "예약", "법률", "법적", "법령", "투자", "장애", "사고", "실수", "민감", "심각", "진지", "잡담", "완료", "구매", "환불"}
	switches := []string{"말투를 바꾸", "말씨를 바꾸", "어투를 바꾸", "말투를 전환", "격식체로", "존댓말로", "반말로", "사극체로", "평어로", "합니다체로", "해요체로", "달리 말한", "달리 쓴"}
	var hits []string
	for _, sentence := range sentences(body) {
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
	for _, sentence := range sentences(body) {
		koreanEmpty := statesEmptiness(sentence) && containsAny(sentence, decisions)
		if (koreanEmpty || statesEnglishEmptiness(sentence)) && instructed(sentence) {
			hits = append(hits, strings.TrimSpace(sentence))
		}
	}
	return hits
}

// sentences splits a layer at its sentence and line boundaries.
func sentences(body string) []string {
	return strings.FieldsFunc(body, func(r rune) bool {
		return strings.ContainsRune(".!?。！？\n", r)
	})
}

// statesEmptiness reports the emptiness a closing line states. "없이"
// ("without") is a negation and no claim, so it does not count.
func statesEmptiness(sentence string) bool {
	normalized := normalize(sentence)
	return strings.Contains(normalized, "없") && !strings.Contains(normalized, "없이")
}

func statesEnglishEmptiness(sentence string) bool {
	normalized := strings.ToLower(normalize(sentence))
	return strings.Contains(normalized, "nothing") && strings.Contains(normalized, "decide") ||
		strings.Contains(normalized, "nodecisiontomake")
}

// instructed reports whether a sentence asks for something rather than
// forbidding it. A layer may state the prohibition itself ("do not switch
// register", "do not end with a nothing-to-decide line"), so a negated predicate
// carries no violation. "없" is not a negation here: it is the emptiness the
// closing line states.
func instructed(sentence string) bool {
	negations := []string{"않", "말고", "금지", "마라", "말아", "아니라", "대신", "배제", "do not", "don't", "never", "avoid", "no need to"}
	return !containsAny(sentence, negations)
}

// containsAny reports whether the text carries one of the tokens, ignoring
// whitespace, punctuation and ASCII case on both sides.
func containsAny(text string, candidates []string) bool {
	normalized := strings.ToLower(normalize(text))
	for _, candidate := range candidates {
		if token := strings.ToLower(normalize(candidate)); token != "" && strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}
