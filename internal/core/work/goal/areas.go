// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"path"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
)

// Areas name repository-relative paths or globs. A matched directory also
// covers its descendants; ** can cross directories. Compare the languages,
// not the files that happen to exist today: workers can create new files.
func areasOverlap(a, b string) bool {
	x, y := areaProgram(a), areaProgram(b)
	if x == nil || y == nil {
		// An uninterpretable legacy declaration cannot prove disjointness.
		return true
	}
	type pair struct{ x, y uint32 }
	queue := []pair{{uint32(x.Start), uint32(y.Start)}}
	seen := map[pair]bool{}
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		i, j := x.Inst[p.x], y.Inst[p.y]
		if i.Op == syntax.InstMatch && j.Op == syntax.InstMatch {
			return true
		}
		if next := areaEpsilon(i); len(next) > 0 {
			for _, pc := range next {
				queue = append(queue, pair{pc, p.y})
			}
			continue
		}
		if next := areaEpsilon(j); len(next) > 0 {
			for _, pc := range next {
				queue = append(queue, pair{p.x, pc})
			}
			continue
		}
		a, b := areaRunes(i), areaRunes(j)
		for len(a) > 0 && len(b) > 0 {
			if a[0] <= b[1] && b[0] <= a[1] {
				queue = append(queue, pair{i.Out, j.Out})
				break
			}
			if a[1] < b[1] {
				a = a[2:]
			} else {
				b = b[2:]
			}
		}
	}
	return false
}

func areaProgram(area string) *syntax.Prog {
	area = path.Clean(area)
	if area == "." {
		area = "**"
	}
	if path.IsAbs(area) || area == ".." || strings.HasPrefix(area, "../") {
		return nil
	}
	if _, err := path.Match(area, ""); err != nil {
		return nil
	}
	runes := []rune(area)
	var re strings.Builder
	re.WriteString("(?s:")
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					re.WriteString("(?:.*/)?")
				} else {
					re.WriteString(".*")
				}
			} else {
				re.WriteString("[^/]*")
			}
		case '?':
			re.WriteString("[^/]")
		case '\\':
			i++ // path.Match above checked the escape.
			re.WriteString(regexp.QuoteMeta(string(runes[i])))
		case '[':
			re.WriteByte('[')
			i++
			if runes[i] == '^' {
				re.WriteByte('^')
				i++
			}
			for ; i < len(runes) && runes[i] != ']'; i++ {
				if runes[i] == '\\' {
					i++
				} else if runes[i] == '-' {
					re.WriteByte('-')
					continue
				}
				// Glob escapes are literal characters, not regexp escapes
				// such as \d or POSIX character classes.
				fmt.Fprintf(&re, `\x{%x}`, runes[i])
			}
			re.WriteByte(']')
		default:
			re.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	re.WriteString(")(?:/.*)?")
	parsed, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return nil
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil
	}
	return program
}

func areaEpsilon(i syntax.Inst) []uint32 {
	switch i.Op {
	case syntax.InstAlt, syntax.InstAltMatch:
		return []uint32{i.Out, i.Arg}
	case syntax.InstNop, syntax.InstCapture, syntax.InstEmptyWidth:
		return []uint32{i.Out}
	case syntax.InstFail, syntax.InstMatch, syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
		return nil
	default:
		return nil
	}
}

func areaRunes(i syntax.Inst) []rune {
	switch i.Op {
	case syntax.InstRune, syntax.InstRune1:
		if len(i.Rune) == 1 {
			return []rune{i.Rune[0], i.Rune[0]}
		}
		return i.Rune
	case syntax.InstRuneAny:
		return []rune{0, unicode.MaxRune}
	case syntax.InstRuneAnyNotNL:
		return []rune{0, '\n' - 1, '\n' + 1, unicode.MaxRune}
	case syntax.InstAlt, syntax.InstAltMatch, syntax.InstNop, syntax.InstCapture, syntax.InstEmptyWidth, syntax.InstFail, syntax.InstMatch:
		return nil
	default:
		return nil
	}
}
