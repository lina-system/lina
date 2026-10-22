// SPDX-License-Identifier: Apache-2.0
package work

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
)

// areaProgram compiles repository-relative globs, including ** as a complete
// path component. Intersection is tested on languages, not existing files:
// leases must cover files a worker has yet to create. A matched directory also
// covers its descendants, including when the directory is matched by a glob.
func areaProgram(glob string) (*syntax.Prog, error) {
	if glob == "" || len(glob) > 512 || strings.ContainsAny(glob, "\\\x00\r\n") {
		return nil, ErrInvalid
	}
	var b strings.Builder
	parts := strings.Split(glob, "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, ErrInvalid
		}
		if p == "**" {
			if i == len(parts)-1 {
				b.WriteString(".*")
			} else {
				b.WriteString("([^/]+/)*")
			}
			continue
		}
		if strings.Contains(p, "**") {
			return nil, ErrInvalid
		}
		runes := []rune(p)
		for j := 0; j < len(runes); j++ {
			switch runes[j] {
			case '*':
				b.WriteString("[^/]*")
			case '?':
				b.WriteString("[^/]")
			case '[':
				end := j + 1
				for end < len(runes) && runes[end] != ']' {
					end++
				}
				if end == len(runes) {
					return nil, ErrInvalid
				}
				cls := string(runes[j : end+1])
				if strings.Contains(cls, "/") {
					return nil, ErrInvalid
				}
				if strings.HasPrefix(cls, "[!") {
					cls = "[^/" + strings.TrimPrefix(cls, "[!")
				} else if strings.HasPrefix(cls, "[^") {
					cls = "[^/" + strings.TrimPrefix(cls, "[^")
				}
				b.WriteString(cls)
				j = end
			default:
				b.WriteString(regexp.QuoteMeta(string(runes[j])))
			}
		}
		if i < len(parts)-1 {
			b.WriteByte('/')
		}
	}
	b.WriteString("(?:/.*)?")
	re, err := syntax.Parse(b.String(), syntax.Perl|syntax.DotNL)
	if err != nil {
		return nil, fmt.Errorf("%w: glob", ErrInvalid)
	}
	return syntax.Compile(re.Simplify())
}
func epsilon(p *syntax.Prog, pc uint32) []uint32 {
	seen := map[uint32]bool{}
	todo := []uint32{pc}
	var out []uint32
	for len(todo) > 0 {
		x := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[x] {
			continue
		}
		seen[x] = true
		in := p.Inst[x]
		switch in.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			todo = append(todo, in.Out, in.Arg)
		case syntax.InstCapture, syntax.InstNop, syntax.InstEmptyWidth:
			todo = append(todo, in.Out)
		case syntax.InstFail:
		case syntax.InstMatch, syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			out = append(out, x)
		}
	}
	return out
}
func runeRanges(in syntax.Inst) []rune {
	switch in.Op {
	case syntax.InstRune, syntax.InstRune1:
		if len(in.Rune) == 1 {
			return []rune{in.Rune[0], in.Rune[0]}
		}
		return in.Rune
	case syntax.InstRuneAny:
		return []rune{0, unicode.MaxRune}
	case syntax.InstRuneAnyNotNL:
		return []rune{0, '\n' - 1, '\n' + 1, unicode.MaxRune}
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstMatch, syntax.InstFail, syntax.InstNop:
	}
	return nil
}
func intersects(a, b *syntax.Prog) bool {
	type pair struct{ x, y uint32 }
	todo := []pair{{uint32(a.Start), uint32(b.Start)}}
	seen := map[pair]bool{}
	for len(todo) > 0 {
		s := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[s] {
			continue
		}
		seen[s] = true
		for _, x := range epsilon(a, s.x) {
			for _, y := range epsilon(b, s.y) {
				ai, bi := a.Inst[x], b.Inst[y]
				if ai.Op == syntax.InstMatch && bi.Op == syntax.InstMatch {
					return true
				}
				ar, br := runeRanges(ai), runeRanges(bi)
				match := false
				for i := 0; i < len(ar); i += 2 {
					for j := 0; j < len(br); j += 2 {
						if max(ar[i], br[j]) <= min(ar[i+1], br[j+1]) {
							match = true
						}
					}
				}
				if match {
					todo = append(todo, pair{ai.Out, bi.Out})
				}
			}
		}
	}
	return false
}
func AreasOverlap(a, b []string) (bool, error) {
	var left, right []*syntax.Prog
	for _, set := range []struct {
		in  []string
		out *[]*syntax.Prog
	}{{a, &left}, {b, &right}} {
		if len(set.in) == 0 {
			return false, ErrInvalid
		}
		for _, glob := range set.in {
			p, err := areaProgram(glob)
			if err != nil {
				return false, err
			}
			*set.out = append(*set.out, p)
		}
	}
	for _, x := range left {
		for _, y := range right {
			if intersects(x, y) {
				return true, nil
			}
		}
	}
	return false, nil
}
