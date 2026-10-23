// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/guard"
)

// Only this word list is checked in. Bodies and queries exist in memory and in
// the disposable canon, never in report values or repository fixtures.
var english = strings.Fields("amber birch cloud delta ember forest garden harbor island journey kettle lantern meadow north ocean paper quiet river silver timber valley willow yellow zebra archive bridge canvas design engine field glass history index journal kernel ledger memory node output person query recall source table update vector window result worker")
var korean = strings.Fields("기억 대화 작업 결과 도구 사람 계획 기록 여행 도시 바다 하늘 나무 시간 음악 책상 연구 자료 검색 저장 확인 질문 답변 연결 목록 변경 요약 내용 문장 생각 경험 흐름 시작 완료 검증 과정 언어 오늘 내일 주말 정원 편지 학교 공부 산책 여름 겨울 아침 저녁")

type message struct {
	ID, Session, Text string
	Entry             guard.Entry
}
type summary struct {
	ID, Session, Text string
	End               string
}
type corpus struct {
	Turns     [][]message
	Summaries []summary
	Skipped   int
}
type counts struct {
	Turns        int `json:"turns"`
	Events       int `json:"events"`
	PersonEvents int `json:"person_events"`
	HangulEvents int `json:"hangul_events"`
	LargeEvents  int `json:"large_events"`
	TextBytes    int `json:"text_bytes"`
	Summaries    int `json:"summaries"`
	SummaryBytes int `json:"summary_bytes"`
	Skipped      int `json:"skipped_source_rows"`
}

func (c corpus) stats() counts {
	s := counts{Turns: len(c.Turns), Summaries: len(c.Summaries), Skipped: c.Skipped}
	for _, turn := range c.Turns {
		for _, m := range turn {
			s.Events++
			s.TextBytes += len(m.Text)
			if m.Entry == guard.EntryOwner {
				s.PersonEvents++
			}
			if len(m.Text) > 16<<10 {
				s.LargeEvents++
			}
			if strings.ContainsFunc(m.Text, isHangul) {
				s.HangulEvents++
			}
		}
	}
	for _, n := range c.Summaries {
		s.SummaryBytes += len(n.Text)
	}
	return s
}
func isHangul(r rune) bool { return r >= 0xac00 && r <= 0xd7a3 }

func generate(turns int) corpus {
	if turns <= 0 {
		return corpus{}
	}
	rng := rand.New(rand.NewPCG(851, 867))
	total := turns*18 + turns*3/10
	c := corpus{}
	index := 0
	for i := 0; i < turns; i++ {
		session := fmt.Sprintf("session-%06d", i*784/1094)
		var turn []message
		for j := 0; j < 18+(i+1)*3/10-i*3/10; j++ {
			// Stratified lengths approximate the published mean and large-body
			// fraction. The unpublished histogram is not claimed as measured.
			q := (index * 7919 % total) * 20011 / total
			size := 256
			switch {
			case q < 292:
				size = 32768
			case q < 2000:
				size = 11000
			case q < 8000:
				size = 2048
			}
			words := english
			if index*37%100 < 26 {
				words = korean
			}
			var text strings.Builder
			fmt.Fprintf(&text, "%06d ", index)
			for text.Len() < size {
				text.WriteString(words[rng.IntN(len(words))])
				text.WriteByte(' ')
			}
			entry := guard.EntryAgent
			if j == 0 {
				entry = guard.EntryOwner
			} else if j%2 == 0 {
				entry = guard.EntryTool
			}
			turn = append(turn, message{fmt.Sprintf("event-%08d", index), session, text.String(), entry})
			index++
		}
		c.Turns = append(c.Turns, turn)
	}
	// Select whole conversation prefixes, at the sample's 18/20011 node ratio.
	want := max(1, (total*18+10005)/20011)
	for i := 0; i < want; i++ {
		turn := c.Turns[i*turns/want]
		last := turn[len(turn)-1]
		var body strings.Builder
		for body.Len() < 7300 {
			body.WriteString(english[rng.IntN(len(english))])
			body.WriteByte(' ')
		}
		c.Summaries = append(c.Summaries, summary{fmt.Sprintf("summary-%06d", i), last.Session, body.String(), last.ID})
	}
	return c
}

// 48 literal substrings and 12 Hangul pairs, as in the research method. A
// snapshot without Hangul reports fewer queries rather than fabricating text.
func (c corpus) queries() []string {
	var texts, pairs []string
	for _, turn := range c.Turns {
		for _, m := range turn {
			if m.Entry != guard.EntryOwner {
				continue
			}
			if utf8.RuneCountInString(strings.TrimSpace(m.Text)) >= 3 {
				texts = append(texts, m.Text)
			}
			runes := []rune(m.Text)
			for i := 1; i < len(runes); i++ {
				if isHangul(runes[i-1]) && isHangul(runes[i]) {
					pairs = append(pairs, string(runes[i-1:i+1]))
					break
				}
			}
		}
	}
	var queries []string
	for i := 0; i < 48 && len(texts) > 0; i++ {
		runes := []rune(strings.TrimSpace(texts[i%len(texts)]))
		n := min(3+i%4, len(runes))
		start := i * 31 % (len(runes) - n + 1)
		q := strings.TrimSpace(string(runes[start : start+n]))
		if utf8.RuneCountInString(q) < 3 {
			q = string(runes[:3])
		}
		queries = append(queries, q)
	}
	for i := 0; i < 12 && len(pairs) > 0; i++ {
		queries = append(queries, pairs[i%len(pairs)])
	}
	return queries
}
