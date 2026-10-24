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
		"korean closing":  {body: "끝났어. 정할 일은 없어.", hits: 1},
		"english closing": {body: "All clear. Nothing to decide now.", hits: 1},
		"open decision":   {body: "남은 결정은 두 가지야.", hits: 0},
		"work still open": {body: "정할 것이 남아 있다.", hits: 0},
		"negated advisal": {body: "결정을 미루라는 말은 하지 않는다.", hits: 0},
	} {
		t.Run("closing/"+name, func(t *testing.T) {
			if hits := nothingToDecide(tc.body); len(hits) != tc.hits {
				t.Fatalf("closing hits = %q, want %d", hits, tc.hits)
			}
		})
	}
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
	sentenceBreak = regexp.MustCompile(`[.!?。！？\n]+`)
	englishEmpty  = regexp.MustCompile(`(?i)nothing\s+(left\s+)?to\s+decide|no\s+decision\s+to\s+make`)
)

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

func containsAny(text string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}
